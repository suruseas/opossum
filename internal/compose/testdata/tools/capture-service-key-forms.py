#!/usr/bin/env python3
"""Capture what docker compose does with one service key written in 13 forms.

    internal/compose/testdata/tools/capture-service-key-forms.py > internal/compose/testdata/service-key-forms.json

For every key the service schema knows (internal/compose/servicespec.json) and each
of 13 values (a string, an integer, a float, a boolean, a list of numbers, a
mapping, null, and the number- and boolean-looking strings "7", "true", "1.5",
"abc", a list of strings and a mapping of strings), `docker compose config` is run
on a one-service file and its answer is classified:

    accept   rc 0
    schema   refused for the shape of the value (`must be a string`, a pattern, ...)
    other    refused for another reason (a name that is not defined, a value that
             does not read into a number, a file that is not there)

The classification, not the message, is what the test reads: the message is kept
to say what docker compose said. Run it in a scratch directory (it writes the
files it asks docker compose about into the current one).
"""
import json, os, re, subprocess, sys, tempfile

here = os.path.dirname(os.path.abspath(__file__))
spec = json.load(open(os.path.join(here, "..", "..", "servicespec.json")))
forms = [("str", '"x"'), ("int", "7"), ("float", "1.5"), ("bool", "true"), ("list", "[1, 2]"), ("map", "{a: 1}"),
         ("null", "null"), ("sint", '"7"'), ("sbool", '"true"'), ("sfloat", '"1.5"'), ("sstr", '"abc"'),
         ("listS", "[a, b]"), ("mapS", "{a: b}")]
schema_words = re.compile(r"must be an? |does not match pattern|unexpected type|is not one of|cannot be negative|must be one of|additional properties|value must be")
rows = []
with tempfile.TemporaryDirectory() as d:
    for key in sorted(spec["properties"]):
        for form, value in forms:
            if key == "image":
                body = "services:\n  web:\n    image: %s\n" % value
            else:
                body = "services:\n  web:\n    image: alpine:3\n    %s: %s\n" % (key, value)
            path = os.path.join(d, "%s__%s.yaml" % (key, form))
            open(path, "w").write(body)
            r = subprocess.run(["docker", "compose", "-f", path, "config"], capture_output=True, text=True)
            msg = (r.stderr or "").strip().split("\n")[0]
            kind = "accept" if r.returncode == 0 else ("schema" if schema_words.search(msg) else "other")
            # The message is kept to say what docker compose said, without where it
            # ran: the directory it was asked in is not part of the answer.
            shown = msg.replace(d + "/", "<dir>/").replace(path, "<file>")
            rows.append({"key": key, "form": form, "value": value, "docker": kind,
                         "msg": shown.split("<file>")[-1][:100] if r.returncode else ""})
json.dump(rows, sys.stdout, indent=0)
