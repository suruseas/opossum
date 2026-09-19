#!/usr/bin/env python3
"""Regenerates docker.json: which services docker compose enables in every
cell of the COMPOSE_PROFILES matrix. The Go test builds the same cells and
holds opossum to it.

    python3 gen.py > docker.json        # needs `docker compose` on PATH

A cell is: whether `--profile a` is given (flag), what the shell says
(shell: unset, "b", or set and empty), what the env file says (env: absent
— no `.env` at all, or a `--env-file` with no such line — "c", or set and empty), and which env file that is (source: the working
directory's `.env`, or `--env-file cf.env` — in which case the `.env` holds
COMPOSE_PROFILES=d, which must not be read).

The extra cells are one-offs: where the compose file is (chosen from another
directory by COMPOSE_FILE or by -f, each directory's `.env` naming another
profile — and then only one of the two saying anything), two --env-files, a `--profile` that names nothing, and how the value is spelled.
"""
import itertools, json, os, subprocess, sys, tempfile

COMPOSE = "services:\n  base:\n    image: alpine:3\n" + "".join(
    "  s%s:\n    image: alpine:3\n    profiles: [%s]\n" % (p, p) for p in "abcd")
STATE = {"absent": None, "value": "%s", "empty": ""}


def run(proj, args, env_extra):
    env = {k: v for k, v in os.environ.items() if not k.startswith("COMPOSE_")}
    env.update(env_extra)
    p = subprocess.run(["docker", "compose"] + args + ["config", "--services"],
                       cwd=proj, env=env, capture_output=True, text=True)
    return {"rc": p.returncode, "services": sorted(p.stdout.split()) if p.returncode == 0 else []}


def line(state, value):
    v = STATE[state]
    return "" if v is None else "COMPOSE_PROFILES=%s\n" % (v % value if v else "")


def main():
    cells = []
    for flag, shell, env, source in itertools.product([False, True], STATE, STATE, ["dotenv", "envfile"]):
        with tempfile.TemporaryDirectory() as root:
            proj = os.path.join(os.path.realpath(root), "proj")
            os.makedirs(proj)
            open(os.path.join(proj, "compose.yaml"), "w").write(COMPOSE)
            args, extra = [], {}
            if source == "dotenv":
                if env != "absent":
                    open(os.path.join(proj, ".env"), "w").write(line(env, "c"))
            else:
                open(os.path.join(proj, ".env"), "w").write("COMPOSE_PROFILES=d\n")
                open(os.path.join(proj, "cf.env"), "w").write(line(env, "c"))
                args += ["--env-file", "cf.env"]
            if flag:
                args += ["--profile", "a"]
            if STATE[shell] is not None:
                extra["COMPOSE_PROFILES"] = "b" if shell == "value" else ""
            cell = {"flag": flag, "shell": shell, "env": env, "source": source}
            cell.update(run(proj, args, extra))
            cells.append(cell)

    extras = []
    # Where the file is: each directory's `.env` names another profile.
    for how in ["composefile", "flag"]:
        with tempfile.TemporaryDirectory() as root:
            proj = os.path.join(os.path.realpath(root), "proj")
            os.makedirs(os.path.join(proj, "sub"))
            open(os.path.join(proj, "compose.yaml"), "w").write("services:\n  main:\n    image: alpine:3\n")
            open(os.path.join(proj, "sub", "x.yaml"), "w").write(COMPOSE)
            open(os.path.join(proj, ".env"), "w").write("COMPOSE_PROFILES=c\n")
            open(os.path.join(proj, "sub", ".env"), "w").write("COMPOSE_PROFILES=d\n")
            args, extra = ([], {"COMPOSE_FILE": "sub/x.yaml"}) if how == "composefile" else (["-f", "sub/x.yaml"], {})
            cell = {"kind": "where", "how": how}
            cell.update(run(proj, args, extra))
            extras.append(cell)
    # …and where only one of the two says anything. For a file COMPOSE_FILE chose,
    # the working directory's `.env` is asked first and the file's directory's
    # second — unless the first sets the variable to nothing, or an --env-file
    # is given; for -f there is the file's directory's and no second.
    for how, cwd, sub in [("composefile", None, "d"), ("composefile", "", "d"), ("composefile", "c", None),
                          ("composefile+envfile", None, "d"), ("flag", "c", None), ("flag", "c", "")]:
        with tempfile.TemporaryDirectory() as root:
            proj = os.path.join(os.path.realpath(root), "proj")
            os.makedirs(os.path.join(proj, "sub"))
            open(os.path.join(proj, "compose.yaml"), "w").write("services:\n  main:\n    image: alpine:3\n")
            open(os.path.join(proj, "sub", "x.yaml"), "w").write(COMPOSE)
            for d, v in [(proj, cwd), (os.path.join(proj, "sub"), sub)]:
                if v is not None:
                    open(os.path.join(d, ".env"), "w").write("COMPOSE_PROFILES=%s\n" % v)
            args, extra = [], {}
            if how == "flag":
                args = ["-f", "sub/x.yaml"]
            else:
                extra["COMPOSE_FILE"] = "sub/x.yaml"
            if how == "composefile+envfile":
                open(os.path.join(proj, "cf.env"), "w").write("OTHER=1\n")
                args = ["--env-file", "cf.env"]
            cell = {"kind": "second", "how": how, "cwd": cwd, "sub": sub}
            cell.update(run(proj, args, extra))
            extras.append(cell)
    # The same, with the two `.env` files written out: a first `.env` that is there
    # and does not say, a --profile beside a second `.env`, and values that refer
    # to a variable — docker compose reads the working directory's first and the
    # file's directory's for what is not set yet, and a value there sees the first.
    for cwdenv, subenv, flags in [("OTHER=1\n", "COMPOSE_PROFILES=d\n", []),
                                  (None, "COMPOSE_PROFILES=d\n", ["a"]),
                                  ("X=a\n", "X=d\nCOMPOSE_PROFILES=${X}\n", []),
                                  ("X=d\n", "COMPOSE_PROFILES=${X}\n", []),
                                  ("X=a\nCOMPOSE_PROFILES=${X}\n", None, [])]:
        with tempfile.TemporaryDirectory() as root:
            proj = os.path.join(os.path.realpath(root), "proj")
            os.makedirs(os.path.join(proj, "sub"))
            open(os.path.join(proj, "compose.yaml"), "w").write("services:\n  main:\n    image: alpine:3\n")
            open(os.path.join(proj, "sub", "x.yaml"), "w").write(COMPOSE)
            for d, body in [(proj, cwdenv), (os.path.join(proj, "sub"), subenv)]:
                if body is not None:
                    open(os.path.join(d, ".env"), "w").write(body)
            args = [a for f in flags for a in ("--profile", f)]
            cell = {"kind": "layered", "cwdenv": cwdenv, "subenv": subenv, "flags": flags}
            cell.update(run(proj, args, {"COMPOSE_FILE": "sub/x.yaml"}))
            extras.append(cell)
    # What a --profile value is: one name, spaces and all.
    for value in [" a ", "a,b"]:
        with tempfile.TemporaryDirectory() as root:
            proj = os.path.join(os.path.realpath(root), "proj")
            os.makedirs(proj)
            open(os.path.join(proj, "compose.yaml"), "w").write(COMPOSE)
            cell = {"kind": "flagvalue", "flags": [value]}
            cell.update(run(proj, ["--profile", value], {}))
            extras.append(cell)
    # --env-file repeats, and the later file wins — or the only one that says.
    for one, two in [("a", "b"), ("a", None), (None, "b"), ("a", "")]:
        with tempfile.TemporaryDirectory() as root:
            proj = os.path.join(os.path.realpath(root), "proj")
            os.makedirs(proj)
            open(os.path.join(proj, "compose.yaml"), "w").write(COMPOSE)
            for name, v in [("one.env", one), ("two.env", two)]:
                open(os.path.join(proj, name), "w").write("OTHER=1\n" if v is None else "COMPOSE_PROFILES=%s\n" % v)
            cell = {"kind": "twoenvfiles", "one": one, "two": two}
            cell.update(run(proj, ["--env-file", "one.env", "--env-file", "two.env"], {}))
            extras.append(cell)
    # A `--profile` that names nothing is still the flag, and still the only source.
    for flags in [[""], ["a", ""], ["", "a"]]:
        for place in ["dotenv", "shell"]:
            with tempfile.TemporaryDirectory() as root:
                proj = os.path.join(os.path.realpath(root), "proj")
                os.makedirs(proj)
                open(os.path.join(proj, "compose.yaml"), "w").write(COMPOSE)
                extra = {}
                if place == "dotenv":
                    open(os.path.join(proj, ".env"), "w").write("COMPOSE_PROFILES=b\n")
                else:
                    extra["COMPOSE_PROFILES"] = "b"
                args = [a for f in flags for a in ("--profile", f)]
                cell = {"kind": "emptyflag", "place": place, "flags": flags}
                cell.update(run(proj, args, extra))
                extras.append(cell)
    # How the value is spelled, in the `.env` and in the shell.
    for spelling in ["a,b", "a, b", " a ,b ", "a,,b", ",", "*", "a,*", "nosuch", "a b", "\"a,b\"", "'a'"]:
        for place in ["dotenv", "shell"]:
            with tempfile.TemporaryDirectory() as root:
                proj = os.path.join(os.path.realpath(root), "proj")
                os.makedirs(proj)
                open(os.path.join(proj, "compose.yaml"), "w").write(COMPOSE)
                extra = {}
                if place == "dotenv":
                    open(os.path.join(proj, ".env"), "w").write("COMPOSE_PROFILES=%s\n" % spelling)
                else:
                    extra["COMPOSE_PROFILES"] = spelling
                cell = {"kind": "spelling", "place": place, "value": spelling}
                cell.update(run(proj, [], extra))
                extras.append(cell)
    version = subprocess.run(["docker", "compose", "version", "--short"], capture_output=True, text=True).stdout.strip()
    json.dump({"docker_compose": version, "cells": cells, "extras": extras}, sys.stdout, indent=1)
    print()


if __name__ == "__main__":
    main()
