#!/usr/bin/env python3
"""Regenerates docker.json: what docker compose makes of every cell of the
COMPOSE_FILE matrix — which project it names and which `.env` it expands
variables from. The Go test builds the same cells and holds opossum to it.

    python3 gen.py > docker.json        # needs `docker compose` on PATH

A cell is: what chose the file (source), where the file is (where), what the
working directory's `.env` says about the project's name (cwdname: absent, a
value, or set and empty) and whether the file's own directory's `.env` names
the project (subname). For source "envfile" the name state is the env file's,
and the working directory's `.env` carries a name that must not be read.
"""
import itertools, json, os, subprocess, sys, tempfile

SOURCES = ["flag", "shell", "dotenv", "envfile"]
WHERES = ["cwd", "other"]
CWDNAMES = ["absent", "value", "empty"]
SUBNAMES = [False, True]
TARGET = 'services:\n  target:\n    image: alpine:3\n    environment: {V: "${VAR:-unset}"}\n'
MAIN = 'services:\n  main:\n    image: alpine:3\n'


def name_line(state, value):
    return {"absent": "", "value": "COMPOSE_PROJECT_NAME=%s\n" % value, "empty": "COMPOSE_PROJECT_NAME=\n"}[state]


def build(root, source, where, cwdname, subname):
    proj = os.path.join(root, "proj")
    os.makedirs(os.path.join(proj, "sub"))
    open(os.path.join(proj, "compose.yaml"), "w").write(MAIN)
    open(os.path.join(proj, "x.yaml"), "w").write(TARGET)
    open(os.path.join(proj, "sub", "x.yaml"), "w").write(TARGET)
    path = "x.yaml" if where == "cwd" else "sub/x.yaml"
    dotenv = "VAR=from-cwd-env\n"
    args, env = [], {}
    if source == "envfile":
        dotenv += "COMPOSE_PROJECT_NAME=dotenvname\n"
        open(os.path.join(proj, "cf.env"), "w").write(
            "VAR=from-envfile\nCOMPOSE_FILE=%s\n" % path + name_line(cwdname, "envfilename"))
        args = ["--env-file", "cf.env"]
    else:
        dotenv += name_line(cwdname, "cwdname")
        if source == "dotenv":
            dotenv += "COMPOSE_FILE=%s\n" % path
        elif source == "shell":
            env["COMPOSE_FILE"] = path
        else:
            args = ["-f", path]
    open(os.path.join(proj, ".env"), "w").write(dotenv)
    open(os.path.join(proj, "sub", ".env"), "w").write(
        "VAR=from-sub-env\n" + ("COMPOSE_PROJECT_NAME=subname\n" if subname else ""))
    return proj, args, env


def main():
    cells = []
    for source, where, cwdname, subname in itertools.product(SOURCES, WHERES, CWDNAMES, SUBNAMES):
        with tempfile.TemporaryDirectory() as root:
            proj, args, extra = build(os.path.realpath(root), source, where, cwdname, subname)
            env = {k: v for k, v in os.environ.items() if not k.startswith("COMPOSE_") and k != "VAR"}
            env.update(extra)
            p = subprocess.run(["docker", "compose"] + args + ["config", "--format", "json"],
                               cwd=proj, env=env, capture_output=True, text=True)
            cell = {"source": source, "where": where, "cwdname": cwdname, "subname": subname, "rc": p.returncode}
            if p.returncode == 0:
                doc = json.loads(p.stdout)
                cell["name"] = doc["name"]
                cell["services"] = sorted(doc["services"])
                cell["V"] = doc["services"]["target"]["environment"]["V"]
            cells.append(cell)
    version = subprocess.run(["docker", "compose", "version", "--short"], capture_output=True, text=True).stdout.strip()
    json.dump({"docker_compose": version, "cells": cells}, sys.stdout, indent=1)
    print()


if __name__ == "__main__":
    main()
