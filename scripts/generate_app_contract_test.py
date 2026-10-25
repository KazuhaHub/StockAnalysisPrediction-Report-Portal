import copy
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

    def test_duplicate_source_keys_refused(self):
        with self.assertRaisesRegex(ValueError, "duplicate source key"):
            contract.render(self.raw.replace(b'"title":', b'"$schema":"other","title":', 1))

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


if __name__ == "__main__":
    unittest.main()
