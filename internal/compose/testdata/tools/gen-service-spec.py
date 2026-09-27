#!/usr/bin/env python3
"""Cut the shape of a service out of the compose-spec JSON schema.

docker compose embeds the schema in its plugin binary; save it to a file (the block
that begins `{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id":
"compose_spec.json"`) and run:

    internal/compose/testdata/tools/gen-service-spec.py compose-spec.json > internal/compose/servicespec.json

What is kept is the structure — types, items, oneOf, properties, patternProperties,
the schema of an additional property, enum, pattern, minimum/maximum and
uniqueItems — with every $ref written out. What is left out is the prose (titles,
descriptions, defaults), `required` and the switch that forbids a key the schema
does not know: opossum keeps its own list of the keys a service may have, and a
newer docker compose may take a key this copy does not.

The schema is the Compose Specification's (https://github.com/compose-spec/compose-spec,
Apache License 2.0); the file this writes keeps its structure and none of its prose.
The list of keys held to it (`heldToTheSchema` in servicespec.go) is not derived from
the schema: it is the keys whose value opossum took as it came, found by writing each
key in the 13 forms of capture-service-key-forms.py and asking docker compose and
opossum. After a new capture, the test says which rows moved.
"""
import json, sys

spec = json.load(open(sys.argv[1]))
defs = spec["$defs"]
KEEP = {"type", "items", "oneOf", "properties", "patternProperties", "enum", "pattern", "minimum", "maximum", "uniqueItems"}


def expand(node, stack=()):
    if isinstance(node, list):
        return [expand(x, stack) for x in node]
    if not isinstance(node, dict):
        return node
    if "$ref" in node:
        name = node["$ref"].split("/")[-1]
        if name in stack:
            raise SystemExit("recursive $ref: " + name)
        return expand(defs[name], stack + (name,))
    out = {}
    for k, v in node.items():
        if k in ("properties", "patternProperties"):
            out[k] = {kk: expand(vv, stack) for kk, vv in v.items()}
        elif k == "additionalProperties":
            if isinstance(v, dict):
                out["additional"] = expand(v, stack)
        elif k in KEEP:
            out[k] = expand(v, stack)
    return out


service = expand(defs["service"])
json.dump(service, sys.stdout, sort_keys=True, separators=(",", ":"))
