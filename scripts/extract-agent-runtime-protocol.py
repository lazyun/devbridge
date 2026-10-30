#!/usr/bin/env python3
"""Extract the consumed contract from installed `codex app-server` JSON schemas.

Usage: extract-agent-runtime-protocol.py GENERATED_DIRECTORY OUTPUT_JSON
Generate the source with the exact installed executable first; this script never
runs Codex, changes capabilities, or updates the checked-in contract implicitly.
"""
import json
import pathlib
import sys

METHODS = (
    "item/commandExecution/requestApproval",
    "item/fileChange/requestApproval",
    "item/permissions/requestApproval",
    "item/tool/requestUserInput",
    "mcpServer/elicitation/request",
)


def strip_documentation(value):
    if isinstance(value, dict):
        return {key: strip_documentation(item) for key, item in value.items()
                if key not in ("title", "description")}
    if isinstance(value, list):
        return [strip_documentation(item) for item in value]
    return value


def extract(source):
    def read(name):
        return json.loads((source / (name + ".json")).read_text())

    def methods(schema):
        return [item["properties"]["method"]["enum"][0]
                for item in schema["oneOf"]]

    requests = read("ServerRequest")
    notifications = read("ServerNotification")
    result = {"methods": {}}
    by_method = {item["properties"]["method"]["enum"][0]: item
                 for item in requests["oneOf"]}
    for method in METHODS:
        name = by_method[method]["properties"]["params"]["$ref"].split("/")[-1]
        result["methods"][method] = {
            "request": strip_documentation(read(name)),
            "response": strip_documentation(read(name.removesuffix("Params") + "Response")),
        }
    result["notifications"] = methods(notifications)
    result["itemTypes"] = [item["properties"]["type"]["enum"][0]
                           for item in notifications["definitions"]["ThreadItem"]["oneOf"]]
    result["serverRequests"] = methods(requests)
    result["clientRequests"] = methods(read("ClientRequest"))
    return result


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    output = pathlib.Path(sys.argv[2])
    output.write_text(json.dumps(extract(pathlib.Path(sys.argv[1])), indent=2) + "\n")
