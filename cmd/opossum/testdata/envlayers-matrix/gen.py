#!/usr/bin/env python3
"""Regenerates docker.json: which `.env` docker compose reads a variable from
when the compose file is in another directory than the one it is run from.

    python3 gen.py > docker.json        # needs `docker compose` on PATH

The compose file is proj/sub/x.yaml and the command runs in proj. A cell is:

  kind  what the variable is: "var" (a ${V} the compose file refers to),
        "name" (COMPOSE_PROJECT_NAME) or "profiles" (COMPOSE_PROFILES)
  how   what chose the file: "composefile" (COMPOSE_FILE in the shell),
        "dotenv" (COMPOSE_FILE in proj/.env), "flag" (-f sub/x.yaml) or
        "envfile" (COMPOSE_FILE in the shell and --env-file cf.env, which says
        nothing of the variable)
  cwd   what proj/.env says of the variable: "nofile", "noline" (the file is
        there, the variable is not), "value" or "empty" (set to nothing)
  sub   what proj/sub/.env says: "nofile", "value" or "empty"

The "direction" cells are about a value that refers to another variable — which
file's variables it sees — the "order" cells about one that refers to a variable
of its own file, and the "shell" cells have the shell say it as well.
What is recorded: the project's name, ${V} as expanded, the services enabled,
and whether docker compose warned that a variable is not set.
"""
import itertools, json, os, subprocess, sys, tempfile

COMPOSE = ('services:\n  base:\n    image: "alpine:${V:-unset}"\n' +
           "".join("  s%s:\n    image: alpine:3\n    profiles: [%s]\n" % (p, p) for p in "cd"))
KEY = {"var": "V", "name": "COMPOSE_PROJECT_NAME", "profiles": "COMPOSE_PROFILES"}
VALUE = {"var": ("cwdv", "subv"), "name": ("cwdname", "subname"), "profiles": ("c", "d")}


def body(kind, state, which):
    if state == "nofile":
        return None
    if state == "noline":
        return "OTHER=1\n"
    return "%s=%s\n" % (KEY[kind], VALUE[kind][which] if state == "value" else "")


def ask(how, cwdenv, subenv, shell=None):
    with tempfile.TemporaryDirectory() as root:
        proj = os.path.join(os.path.realpath(root), "proj")
        os.makedirs(os.path.join(proj, "sub"))
        open(os.path.join(proj, "compose.yaml"), "w").write("services:\n  main:\n    image: alpine:3\n")
        open(os.path.join(proj, "sub", "x.yaml"), "w").write(COMPOSE)
        args, extra = [], dict(shell or {})
        if how == "dotenv":
            cwdenv = (cwdenv or "") + "COMPOSE_FILE=sub/x.yaml\n"
        elif how == "flag":
            args = ["-f", "sub/x.yaml"]
        else:
            extra["COMPOSE_FILE"] = "sub/x.yaml"
        if how == "envfile":
            open(os.path.join(proj, "cf.env"), "w").write("OTHER=1\n")
            args = ["--env-file", "cf.env"]
        for d, b in [(proj, cwdenv), (os.path.join(proj, "sub"), subenv)]:
            if b is not None:
                open(os.path.join(d, ".env"), "w").write(b)
        env = {k: v for k, v in os.environ.items() if not k.startswith("COMPOSE_") and k not in ("V", "X")}
        env.update(extra)
        p = subprocess.run(["docker", "compose"] + args + ["config", "--format", "json"],
                           cwd=proj, env=env, capture_output=True, text=True)
        out = {"rc": p.returncode, "warned": "variable is not set" in p.stderr}
        if p.returncode == 0:
            doc = json.loads(p.stdout)
            out.update(name=doc["name"], services=sorted(doc["services"]),
                       V=doc["services"]["base"]["image"].split(":", 1)[1])
        return out


def main():
    cells = []
    for kind, how, cwd, sub in itertools.product(KEY, ["composefile", "dotenv", "flag", "envfile"],
                                                 ["nofile", "noline", "value", "empty"], ["nofile", "value", "empty"]):
        cell = {"kind": kind, "how": how, "cwd": cwd, "sub": sub}
        cell.update(ask(how, body(kind, cwd, 0), body(kind, sub, 1)))
        cells.append(cell)
    extras = []
    for kind in KEY:
        k, (cv, sv) = KEY[kind], VALUE[kind]
        for label, cwdenv, subenv in [
                ("sub sees cwd", "X=%s\n" % cv, "%s=${X}\n" % k),
                ("cwd does not see sub", "%s=${X}\n" % k, "X=%s\n" % sv),
                ("cwd wins, and sees itself", "X=%s\n%s=${X}\n" % (cv, k), "X=%s\n" % sv),
                ("sub sees cwd over itself", "X=%s\n" % cv, "X=%s\n%s=${X}\n" % (sv, k))]:
            cell = {"group": "direction", "kind": kind, "label": label, "cwdenv": cwdenv, "subenv": subenv}
            cell.update(ask("composefile", cwdenv, subenv))
            extras.append(cell)
        # A value in the second file that refers to a variable of that same file:
        # it sees the lines above it, not the ones below, and the working
        # directory's over its own.
        for label, cwdenv, subenv in [
                ("sub sees its own line above", "OTHER=1\n", "Y=%s\n%s=${Y}\n" % (sv, k)),
                ("sub does not see its own line below", "OTHER=1\n", "%s=${Y}\nY=%s\n" % (k, sv)),
                ("sub sees cwd over its own line above", "Y=%s\n" % cv, "Y=%s\n%s=${Y}\n" % (sv, k))]:
            cell = {"group": "order", "kind": kind, "label": label, "cwdenv": cwdenv, "subenv": subenv}
            cell.update(ask("composefile", cwdenv, subenv))
            extras.append(cell)
        for label, shellvalue in [("shell value", "shellv" if kind != "profiles" else "c"), ("shell empty", "")]:
            cwdenv, subenv = body(kind, "noline", 0), body(kind, "value", 1)
            cell = {"group": "shell", "kind": kind, "label": label, "cwdenv": cwdenv, "subenv": subenv, "shell": {k: shellvalue}}
            cell.update(ask("composefile", cwdenv, subenv, {k: shellvalue}))
            extras.append(cell)
    version = subprocess.run(["docker", "compose", "version", "--short"], capture_output=True, text=True).stdout.strip()
    json.dump({"docker_compose": version, "cells": cells, "extras": extras}, sys.stdout, indent=1)
    print()


if __name__ == "__main__":
    main()
