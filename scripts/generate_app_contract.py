#!/usr/bin/env python3
"""Derive experimental Go/SDK descriptor products from one source schema."""

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
PORTAL_MODE = (ROOT / "internal/appplatform").is_dir()
SOURCE = ROOT / ("internal/appplatform/contract/descriptor-v2.schema.json" if PORTAL_MODE else "packages/app-sdk/schema/descriptor-v2.schema.json")
HEADER = "Derived from the Portal descriptor schema; edit the source contract instead."
ALLOWED = {
    "$schema", "$id", "$ref", "$defs", "title", "description", "type", "const",
    "enum", "properties", "required", "additionalProperties", "items", "minItems",
    "maxItems", "uniqueItems", "minLength", "maxLength", "pattern", "minimum",
    "maximum", "anyOf", "allOf", "not", "x-go-name", "x-go-pointer",
    "x-go-omitempty", "x-typescript-name", "x-portal-parser-limits",
}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate source key: {key}")
        result[key] = value
    return result


def validate_node(node):
    if not isinstance(node, dict) or set(node) - ALLOWED:
        raise ValueError("unsupported contract keyword or non-object schema")
    for name in ("$defs", "properties"):
        for child in node.get(name, {}).values():
            validate_node(child)
    for name in ("items", "not"):
        if name in node:
            validate_node(node[name])
    for name in ("anyOf", "allOf"):
        for child in node.get(name, []):
            validate_node(child)
    if node.get("type") == "object":
        if node.get("additionalProperties") is not False:
            raise ValueError("descriptor objects must be closed")
        required = node.get("required", [])
        if len(required) != len(set(required)) or set(required) - set(node["properties"]):
            raise ValueError("required field is duplicated or absent")


def without_null(node):
    if "anyOf" not in node:
        return node
    candidates = [item for item in node["anyOf"] if item != {"type": "null"}]
    if len(node["anyOf"]) != 2 or len(candidates) != 1:
        raise ValueError("only explicit nullable unions are supported")
    return candidates[0]


def go_type(node):
    base = without_null(node)
    if "$ref" in base:
        name = base["$ref"].removeprefix("#/$defs/")
        if name == base["$ref"]:
            raise ValueError("external schema references are forbidden")
        return ("*" if node.get("x-go-pointer") else "") + name
    if "const" in base:
        if not isinstance(base["const"], str):
            raise ValueError("unsupported constant type")
        return "string"
    if base.get("enum") == [False, None]:
        return "bool"
    kind = base.get("type")
    if kind == "array":
        return "[]" + go_type(base["items"])
    if kind in ("string", "integer"):
        return ("*" if node.get("x-go-pointer") else "") + {"string": "string", "integer": "int"}[kind]
    raise ValueError("unsupported generated Go field type")


def ts_type(node, definitions):
    if "anyOf" in node:
        return " | ".join(ts_type(item, definitions) for item in node["anyOf"])
    if "$ref" in node:
        name = node["$ref"].removeprefix("#/$defs/")
        return definitions[name].get("x-typescript-name", name)
    if "const" in node:
        return json.dumps(node["const"])
    if "enum" in node:
        return " | ".join(json.dumps(value) for value in node["enum"])
    kind = node.get("type")
    if kind == "array":
        return "(" + ts_type(node["items"], definitions) + ")[]"
    if kind in ("string", "integer", "null"):
        return {"string": "string", "integer": "number", "null": "null"}[kind]
    raise ValueError("unsupported generated TypeScript field type")


def render(raw):
    schema = json.loads(raw, object_pairs_hook=unique_object)
    validate_node(schema)
    if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" or schema["$ref"] != "#/$defs/Manifest":
        raise ValueError("unsupported schema dialect/root")
    definitions = schema["$defs"]
    digest = hashlib.sha256(raw).hexdigest()
    go = ["// " + HEADER, "package appplatform", 'import "regexp"',
          f'const descriptorContractSHA256 = "{digest}"']
    ts = ["// " + HEADER, f'export const DESCRIPTOR_SCHEMA_SHA256 = "{digest}";']
    fields = {}
    for name, definition in definitions.items():
        if definition.get("type") != "object":
            raise ValueError("named contract definitions must be objects")
        go.append(f"type {name} struct {{")
        ts.append(f'export interface {definition.get("x-typescript-name", name)} {{')
        fields[name] = list(definition["properties"])
        for key, field in definition["properties"].items():
            go_name = field.get("x-go-name", "".join("ID" if part == "id" else part.capitalize() for part in key.split("_")))
            tag = key + (",omitempty" if field.get("x-go-omitempty") else "")
            go.append(f' {go_name} {go_type(field)} `json:"{tag}"`')
            optional = "" if key in definition["required"] else "?"
            ts.append(f" {key}{optional}: {ts_type(field, definitions)};")
        go.append("}")
        ts.append("}")
    p = lambda name, key: definitions[name]["properties"][key]
    for left, right, keys in (
        (p("Manifest", "id"), p("Manifest", "publisher"), ("pattern",)),
        (p("Presentation", "name_key"), p("Presentation", "description_key"), ("pattern",)),
        (p("Presentation", "entry"), p("Artifact", "artifact"), ("pattern", "minLength", "maxLength")),
        (p("Compatibility", "platform_api"), p("Compatibility", "runner_protocol"), ("pattern", "minLength", "maxLength", "allOf")),
        (p("DataCompatibility", "schema"), p("DataCompatibility", "readable_schemas")["items"], ("type", "minimum", "maximum")),
    ):
        if any(left.get(key) != right.get(key) for key in keys):
            raise ValueError("shared validator domains differ; split the generator and validator explicitly")

    limits = {
        "MaxDescriptorBytes": schema["x-portal-parser-limits"]["bytes"],
        "MaxAppNameCharacters": p("AppDisplayText", "name")["maxLength"],
        "MaxAppDescriptionCharacters": p("AppDisplayText", "description")["maxLength"],
        "maxDescriptorDepth": schema["x-portal-parser-limits"]["depth"],
        "maxRouteQueryFields": without_null(p("Route", "query"))["maxItems"],
        "maxRouteQueryTextBytes": p("RouteQuery", "max_bytes")["maximum"],
        "maxRouteQueryInteger": p("RouteQuery", "max")["maximum"],
        "maxRouteQueryValues": p("RouteQuery", "values")["maxItems"],
        "maxDescriptorRoutes": p("Presentation", "routes")["maxItems"],
        "maxDescriptorReadableSchemas": p("DataCompatibility", "readable_schemas")["maxItems"],
        "minDescriptorDataSchema": p("DataCompatibility", "schema")["minimum"],
        "maxDescriptorDataSchema": p("DataCompatibility", "schema")["maximum"],
        "maxDescriptorPathBytes": p("Presentation", "entry")["maxLength"],
        "maxBackendImageBytes": p("Backend", "image")["maxLength"],
        "maxDescriptorVersionBytes": p("Manifest", "version")["maxLength"],
    }
    for key in ("memory_mib", "cpu_millis", "pids"):
        field = p("Resources", key)
        for prefix, bound in (("min", "minimum"), ("max", "maximum")):
            limits[prefix + "Backend" + field["x-go-name"]] = field[bound]
    for key, value in limits.items():
        if type(value) is not int or value < 1:
            raise ValueError("invalid positive contract limit")
        go.append(f"const {key} = {value}")
    ts.append("export const DESCRIPTOR_LIMITS = " + json.dumps(limits, indent=2) + " as const;")
    constants = {
        "descriptorFormat": p("Manifest", "format")["const"],
        "descriptorBackendKind": p("Backend", "kind")["const"],
        "descriptorBackendProtocol": p("Backend", "protocol")["const"],
    }
    for key, value in constants.items():
        go.append(f"const {key} = {json.dumps(value)}")
    ts.append("export const DESCRIPTOR_CONSTANTS = " + json.dumps(constants, indent=2) + " as const;")
    patterns = {
        "queryName": p("RouteQuery", "name")["pattern"],
        "queryEnum": p("RouteQuery", "values")["items"]["pattern"],
        "slug": p("Manifest", "id")["pattern"],
        "key": schema["x-portal-parser-limits"]["key_pattern"],
        "copyKey": p("Presentation", "name_key")["pattern"],
        "digest": p("Artifact", "sha256")["pattern"],
        "version": p("Manifest", "version")["pattern"],
        "range": p("Compatibility", "platform_api")["pattern"],
        "image": p("Backend", "image")["pattern"],
        "path": p("Presentation", "entry")["pattern"],
    }
    for key, value in patterns.items():
        if any(char in value for char in "`\r\n"):
            raise ValueError("regexp source requires escaped controls and no literal backticks")
        go.append(f"var {key}Pattern = regexp.MustCompile(`{value}`)")
    ts.append("export const DESCRIPTOR_PATTERNS = " + json.dumps(patterns, indent=2) + " as const;")
    capabilities = without_null(p("Capabilities", "required"))["items"]["enum"]
    if capabilities != without_null(p("Capabilities", "optional"))["items"]["enum"]:
        raise ValueError("required and optional capability domains differ")
    for go_name, ts_name, values in (
        ("descriptorKnownCapabilities", "DESCRIPTOR_CAPABILITIES", capabilities),
        ("descriptorLocaleNames", "DESCRIPTOR_LOCALES", p("Presentation", "locales")["items"]["enum"]),
        ("descriptorDisplayIcons", "DESCRIPTOR_DISPLAY_ICONS", p("AppDisplay", "icon")["enum"]),
        ("descriptorPlatformNames", "DESCRIPTOR_PLATFORMS", p("Backend", "platforms")["items"]["enum"]),
        ("descriptorLayoutNames", "DESCRIPTOR_LAYOUTS", p("Route", "layout")["enum"]),
    ):
        if len(values) != len(set(values)) or not all(isinstance(value, str) for value in values):
            raise ValueError("invalid contract enum")
        entries = ",".join(json.dumps(value) + ":true" for value in values)
        go.append(f"var {go_name} = map[string]bool{{{entries}}}")
        ts.append(f"export const {ts_name} = " + json.dumps(values, indent=2) + " as const;")
    ts.append("export const DESCRIPTOR_FIELDS = " + json.dumps(fields, indent=2) + " as const;")
    return ("\n".join(go)+"\n").encode(), ("\n".join(ts)+"\n").encode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="refuse drift without writing")
    parser.add_argument("--apps-dir", type=Path, help="also derive products in an existing Apps checkout")
    args = parser.parse_args()
    raw = SOURCE.read_bytes()
    go, ts = render(raw)
    outputs = {}
    if PORTAL_MODE:
        outputs[ROOT/"web/src/lib/descriptor-contract.generated.ts"] = ts
        # Pure stdin/stdout formatting, without a shell or fetched tool.
        outputs[ROOT/"internal/appplatform/descriptor_contract_generated.go"] = subprocess.run(
            ["gofmt"], input=go, check=True, stdout=subprocess.PIPE).stdout
    elif args.apps_dir:
        raise ValueError("--apps-dir is only available from the Portal source checkout")
    apps = args.apps_dir.resolve() if args.apps_dir else (None if PORTAL_MODE else ROOT)
    if apps:

        sdk = apps/"packages/app-sdk"
        if not (sdk/"package.json").is_file() or (PORTAL_MODE and apps == ROOT):
            raise ValueError("--apps-dir must point to an existing Apps SDK checkout")
        outputs[sdk/"src/descriptor-contract.generated.ts"] = ts
        outputs[sdk/"schema/descriptor-v2.schema.json"] = raw
        outputs[apps/"scripts/generate_app_contract.py"] = Path(__file__).read_bytes()
        pin_path = sdk/"tests/fixtures/bridge-v2-source.json"
        if PORTAL_MODE and (ROOT/"web/src/lib/bridge-v2.ts").is_file():
            bridge = (ROOT/"web/src/lib/bridge-v2.ts").read_bytes()
            tests = (ROOT/"web/src/lib/bridge-v2.test.ts").read_text()
            if tests.count('from "./bridge-v2"') != 1:
                raise ValueError("bridge test import must have one explicit source module")
            tests = tests.replace('from "./bridge-v2"','from "../src/bridge-v2"').encode()
            outputs[sdk/"src/bridge-v2.ts"] = bridge
            outputs[sdk/"tests/bridge-v2.test.ts"] = tests
            outputs[pin_path] = (json.dumps({
                "format":"portal.bridge.source/1",
                "protocol_sha256":hashlib.sha256(bridge).hexdigest(),
                "test_sha256":hashlib.sha256(tests).hexdigest(),
            },indent=2)+"\n").encode()
        elif not PORTAL_MODE:
            package = json.loads((sdk/"package.json").read_bytes(), object_pairs_hook=unique_object)
            if "./bridge-v2" in package.get("exports",{}) or pin_path.exists():
                pin = json.loads(pin_path.read_bytes(), object_pairs_hook=unique_object)
                if not isinstance(pin,dict) or set(pin)!={"format","protocol_sha256","test_sha256"} or pin["format"]!="portal.bridge.source/1":
                    raise ValueError("invalid bridge source pin")
                for key,path in (("protocol_sha256",sdk/"src/bridge-v2.ts"),("test_sha256",sdk/"tests/bridge-v2.test.ts")):
                    if hashlib.sha256(path.read_bytes()).hexdigest()!=pin[key]:
                        raise ValueError("bridge source differs; regenerate from the Portal checkout")
        query_pin = sdk/"tests/fixtures/route-query-v2-source.json"
        if PORTAL_MODE and (ROOT/"web/src/lib/route-query-v2.ts").is_file():
            source = (ROOT/"web/src/lib/route-query-v2.ts").read_bytes()
            tests = (ROOT/"web/src/lib/route-query-v2.test.ts").read_text()
            for quote in ('"', "'"):
                tests = tests.replace("from "+quote+"./route-query-v2"+quote,"from "+quote+"../src/route-query-v2"+quote)
            tests = tests.encode()
            outputs[sdk/"src/route-query-v2.ts"] = source
            outputs[sdk/"tests/route-query-v2.test.ts"] = tests
            outputs[query_pin] = (json.dumps({"format":"portal.query.source/1","source_sha256":hashlib.sha256(source).hexdigest(),"test_sha256":hashlib.sha256(tests).hexdigest()},indent=2)+"\n").encode()
        elif not PORTAL_MODE and (query_pin.exists() or (sdk/"src/route-query-v2.ts").exists() or ((sdk/"src/bridge-v2.ts").exists() and b"./route-query-v2" in (sdk/"src/bridge-v2.ts").read_bytes())):
            pin = json.loads(query_pin.read_bytes(),object_pairs_hook=unique_object)
            if set(pin)!={"format","source_sha256","test_sha256"} or pin["format"]!="portal.query.source/1":
                raise ValueError("invalid query source pin")
            for key,path in (("source_sha256",sdk/"src/route-query-v2.ts"),("test_sha256",sdk/"tests/route-query-v2.test.ts")):
                if hashlib.sha256(path.read_bytes()).hexdigest()!=pin[key]:
                    raise ValueError("query source differs; regenerate from the Portal checkout")
    stale = [str(path) for path, expected in outputs.items() if not path.is_file() or path.read_bytes() != expected]
    if args.check:
        if stale:
            raise ValueError("contract products differ: " + ", ".join(stale))
    else:
        for path, expected in outputs.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(expected)
    print("Descriptor contract products match." if args.check else "Descriptor contract products updated.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
