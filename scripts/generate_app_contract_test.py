import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("generate_app_contract.py")
SPEC = importlib.util.spec_from_file_location("app_contract", SCRIPT)
contract = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(contract)


class AppContractTests(unittest.TestCase):
    def setUp(self):
        self.raw = contract.SOURCE.read_bytes()
        self.schema = json.loads(self.raw)

    def test_reproducible_and_checked_in(self):
        self.assertEqual(contract.render(self.raw), contract.render(self.raw))
        subprocess.run([sys.executable, "-B", str(SCRIPT), "--check"], check=True, capture_output=True)

    def test_optional_numeric_presence_is_preserved(self):
        self.assertEqual(contract.go_type({"type":"integer","x-go-pointer":True}),"*int")

    def test_duplicate_source_keys_refused(self):
        with self.assertRaisesRegex(ValueError, "duplicate source key"):
            contract.render(self.raw.replace(b'"title":', b'"$schema":"other","title":', 1))

    def test_regexp_products_use_readable_go_raw_literals(self):
        go,_ = contract.render(self.raw)
        self.assertIn(b"regexp.MustCompile(`",go)
        self.assertNotIn(b"\\\\.",go)

    def test_regexp_literal_controls_refused(self):
        for char in ("`","\r","\n"):
            self.schema["x-portal-parser-limits"]["key_pattern"] = "^a"+char+"$"
            with self.assertRaisesRegex(ValueError,"escaped controls"):
                contract.render(json.dumps(self.schema).encode())

    def test_unknown_assertion_refused(self):
        self.schema["$defs"]["Route"]["properties"]["path"]["format"] = "uri"
        with self.assertRaisesRegex(ValueError, "unsupported contract keyword"):
            contract.render(json.dumps(self.schema).encode())

    def test_external_reference_refused_without_fetch(self):
        self.schema["$defs"]["Manifest"]["properties"]["frontend"]["$ref"] = "https://example.invalid/schema"
        with self.assertRaisesRegex(ValueError, "external schema references"):
            contract.render(json.dumps(self.schema).encode())

    def test_divergent_shared_domain_refused(self):
        self.schema["$defs"]["Artifact"]["properties"]["artifact"]["maxLength"] = 257
        with self.assertRaisesRegex(ValueError, "shared validator domains differ"):
            contract.render(json.dumps(self.schema).encode())

    def test_capability_domain_difference_refused(self):
        node = self.schema["$defs"]["Capabilities"]["properties"]["optional"]["anyOf"][0]
        node["items"]["enum"] = copy.copy(node["items"]["enum"][:-1])
        with self.assertRaisesRegex(ValueError, "capability domains differ"):
            contract.render(json.dumps(self.schema).encode())

    def test_apps_check_is_read_only_and_needs_no_go_or_network(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "scripts/generate_app_contract.py"
            schema = root / "packages/app-sdk/schema/descriptor-v2.schema.json"
            script.parent.mkdir(parents=True)
            schema.parent.mkdir(parents=True)
            script.write_bytes(SCRIPT.read_bytes())
            schema.write_bytes(self.raw)
            (schema.parent.parent/"package.json").write_text("{}")
            env = {**os.environ, "PATH": "", "PYTHONDONTWRITEBYTECODE": "1"}
            command = [sys.executable, "-B", str(script)]
            missing = subprocess.run(command+["--check"], env=env, capture_output=True)
            self.assertNotEqual(missing.returncode, 0)
            target = root/"packages/app-sdk/src/descriptor-contract.generated.ts"
            self.assertFalse(target.exists())
            subprocess.run(command, env=env, check=True, capture_output=True)
            subprocess.run(command+["--check"], env=env, check=True, capture_output=True)
            target.write_bytes(target.read_bytes()+b"// stale\n")
            before = target.read_bytes()
            stale = subprocess.run(command+["--check"], env=env, capture_output=True)
            self.assertNotEqual(stale.returncode, 0)
            self.assertEqual(target.read_bytes(), before)
            self.assertFalse((root/"internal").exists())

    def test_query_pin_detects_changed_and_removed_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);sdk=root/"packages/app-sdk";script=root/"scripts/generate_app_contract.py"
            script.parent.mkdir(parents=True);(sdk/"schema").mkdir(parents=True);(sdk/"src").mkdir();(sdk/"tests/fixtures").mkdir(parents=True)
            script.write_bytes(SCRIPT.read_bytes());(sdk/"schema/descriptor-v2.schema.json").write_bytes(self.raw);(sdk/"package.json").write_text("{}")
            source=sdk/"src/route-query-v2.ts";tests=sdk/"tests/route-query-v2.test.ts"
            source.write_bytes(b"source");tests.write_bytes(b"tests")
            pin=sdk/"tests/fixtures/route-query-v2-source.json"
            pin.write_text(json.dumps({"format":"portal.query.source/1","source_sha256":hashlib.sha256(source.read_bytes()).hexdigest(),"test_sha256":hashlib.sha256(tests.read_bytes()).hexdigest()}))
            command=[sys.executable,"-B",str(script)];subprocess.run(command,check=True,capture_output=True)
            subprocess.run(command+["--check"],check=True,capture_output=True)
            source.write_bytes(b"changed")
            self.assertNotEqual(subprocess.run(command+["--check"],capture_output=True).returncode,0)
            source.unlink()
            self.assertNotEqual(subprocess.run(command+["--check"],capture_output=True).returncode,0)

    def test_page_pin_detects_changed_and_removed_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);sdk=root/"packages/app-sdk";script=root/"scripts/generate_app_contract.py"
            script.parent.mkdir(parents=True);(sdk/"schema").mkdir(parents=True);(sdk/"src").mkdir();(sdk/"tests/fixtures").mkdir(parents=True)
            script.write_bytes(SCRIPT.read_bytes());(sdk/"schema/descriptor-v2.schema.json").write_bytes(self.raw);(sdk/"package.json").write_text("{}")
            source=sdk/"src/page-v2.ts";tests=sdk/"tests/page-v2.test.ts"
            source.write_bytes(b"source");tests.write_bytes(b"tests")
            pin=sdk/"tests/fixtures/page-v2-source.json"
            pin.write_text(json.dumps({"format":"portal.page.source/1","source_sha256":hashlib.sha256(source.read_bytes()).hexdigest(),"test_sha256":hashlib.sha256(tests.read_bytes()).hexdigest()}))
            command=[sys.executable,"-B",str(script)];subprocess.run(command,check=True,capture_output=True)
            subprocess.run(command+["--check"],check=True,capture_output=True)
            source.write_bytes(b"changed")
            self.assertNotEqual(subprocess.run(command+["--check"],capture_output=True).returncode,0)
            source.unlink()
            self.assertNotEqual(subprocess.run(command+["--check"],capture_output=True).returncode,0)

    def test_apps_bridge_export_requires_pinned_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root/"scripts/generate_app_contract.py"
            sdk = root/"packages/app-sdk"
            script.parent.mkdir(parents=True)
            (sdk/"schema").mkdir(parents=True)
            script.write_bytes(SCRIPT.read_bytes())
            (sdk/"schema/descriptor-v2.schema.json").write_bytes(self.raw)
            package = sdk/"package.json"
            package.write_text("{}")
            command = [sys.executable,"-B",str(script)]
            subprocess.run(command,check=True,capture_output=True)
            package.write_text(json.dumps({"exports":{"./bridge-v2":"./src/bridge-v2.ts"}}))
            result = subprocess.run(command+["--check"],capture_output=True)
            self.assertNotEqual(result.returncode,0,"exported bridge silently escaped its source pin")
            source = sdk/"src/bridge-v2.ts"
            tests = sdk/"tests/bridge-v2.test.ts"
            tests.parent.mkdir(parents=True)
            source.write_bytes(b"public source fixture\n")
            tests.write_bytes(b"public test fixture\n")
            pin = sdk/"tests/fixtures/bridge-v2-source.json"
            pin.parent.mkdir()
            pin.write_text(json.dumps({"format":"portal.bridge.source/1",
                "protocol_sha256":hashlib.sha256(source.read_bytes()).hexdigest(),
                "test_sha256":hashlib.sha256(tests.read_bytes()).hexdigest()}))
            subprocess.run(command+["--check"],check=True,capture_output=True)
            for path in (source,tests):
                original = path.read_bytes()
                path.write_bytes(original+b"changed\n")
                result = subprocess.run(command+["--check"],capture_output=True)
                self.assertNotEqual(result.returncode,0)
                self.assertEqual(path.read_bytes(),original+b"changed\n")
                path.write_bytes(original)


if __name__ == "__main__":
    unittest.main()
