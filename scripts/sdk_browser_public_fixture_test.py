import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("sdk_public_fixture", Path(__file__).parent / "acceptance/sdk_backend_browser/public_fixture.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class PublicBrowserFixtureTests(unittest.TestCase):
    def test_only_public_runtime_inputs_are_readable_without_becoming_writable(self):
        with tempfile.TemporaryDirectory() as folder:
            source = Path(folder)
            build, packages, browsers = source / "build", source / "packages", source / "browsers"
            for path in (build / "host", packages / "playwright", packages / "playwright-core", browsers):
                path.mkdir(parents=True)
            (build / "frontend.zip").write_bytes(b"public frontend")
            (build / "host/host.js").write_text("public host")
            (build / "private-marker").write_text("must not be copied")
            (packages / "private-marker").write_text("must not be copied")
            for name in ("playwright", "playwright-core"):
                (packages / name / "index.js").write_text("public runtime")
            binary = browsers / "chrome"
            binary.write_bytes(b"public executable")
            binary.chmod(0o755)
            runner = source / "run.cjs"
            runner.write_text("public runner")
            root = fixture.prepare(build, packages, browsers, runner)
            try:
                self.assertEqual((root / "frontend.zip").read_bytes(), b"public frontend")
                self.assertFalse(list(root.rglob("private-marker")))
                for path in root.rglob("*"):
                    self.assertEqual(path.stat().st_mode & 0o222, 0)
                    self.assertTrue(path.stat().st_mode & 0o004)
                self.assertEqual((root / "browsers/chrome").stat().st_mode & 0o111, 0o111)
                self.assertEqual(binary.stat().st_mode & 0o777, 0o755)
                (root / "fixture.json").chmod(0o600)
                (root / "fixture.json").write_text('{"format":"foreign"}')
                with self.assertRaisesRegex(ValueError, "ownership marker"):
                    fixture.cleanup(root)
                (root / "fixture.json").write_text('{"format":"portal.sdk.browser.fixture/1"}')
            finally:
                fixture.cleanup(root)
            self.assertFalse(root.exists())

    def test_foreign_directory_cleanup_is_refused(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            with self.assertRaisesRegex(ValueError, "foreign"):
                fixture.cleanup(root)
            self.assertTrue(root.is_dir())


if __name__ == "__main__":
    unittest.main()
