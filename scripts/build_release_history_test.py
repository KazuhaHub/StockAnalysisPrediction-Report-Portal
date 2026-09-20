import tempfile
import unittest
from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path


SCRIPT = Path(__file__).with_name("build-release-history.py")
SPEC = spec_from_file_location("build_release_history", SCRIPT)
MODULE = module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(MODULE)


class BuildReleaseHistoryTest(unittest.TestCase):
    def test_sorts_calver_stops_at_target_and_keeps_only_the_requested_count(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            notes = root / "docs" / "releases" / "2026"
            notes.mkdir(parents=True)
            (notes / "v2026.38.md").write_text("# v2026.38 — First\n\nBody", encoding="utf-8")
            (notes / "v2026.38.2.md").write_text("# v2026.38.2 — Third\n", encoding="utf-8")
            (notes / "v2026.38.1.md").write_text("# v2026.38.1 — Second\n", encoding="utf-8")
            (notes / "v2026.39.md").write_text("# v2026.39 — Future\n", encoding="utf-8")
            items = MODULE.build(root, "v2026.38.2", 2)
            self.assertEqual([item["tag"] for item in items], ["v2026.38.2", "v2026.38.1"])
            self.assertEqual(items[-1]["title"], "Second")

    def test_defaults_to_the_ten_most_recent_notes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            notes = root / "docs" / "releases" / "2026"
            notes.mkdir(parents=True)
            for revision in range(1, 13):
                tag = f"v2026.38.{revision}"
                (notes / f"{tag}.md").write_text(f"# {tag} — Release {revision}\n", encoding="utf-8")
            items = MODULE.build(root, "v2026.38.12")
            self.assertEqual(len(items), 10)
            self.assertEqual(items[0]["tag"], "v2026.38.12")
            self.assertEqual(items[-1]["tag"], "v2026.38.3")

    def test_requires_the_target_note(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "docs" / "releases" / "2026").mkdir(parents=True)
            with self.assertRaisesRegex(ValueError, "does not contain target"):
                MODULE.build(root, "v2026.38")

    def test_rejects_a_non_positive_limit(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(ValueError, "must be positive"):
                MODULE.build(Path(tmp), "v2026.38", 0)


if __name__ == "__main__":
    unittest.main()
