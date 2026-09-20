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
    @staticmethod
    def maturities(*tags):
        return {tag: "beta" for tag in tags}

    def test_sorts_calver_stops_at_target_and_keeps_only_the_requested_count(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            notes = root / "docs" / "releases" / "2026"
            notes.mkdir(parents=True)
            (notes / "v2026.38.md").write_text("# v2026.38 — First\n\nBody", encoding="utf-8")
            (notes / "v2026.38.2.md").write_text("# v2026.38.2 — Third\n", encoding="utf-8")
            (notes / "v2026.38.1.md").write_text("# v2026.38.1 — Second\n", encoding="utf-8")
            (notes / "v2026.39.md").write_text("# v2026.39 — Future\n", encoding="utf-8")
            items = MODULE.build(
                root,
                "v2026.38.2",
                self.maturities("v2026.38", "v2026.38.1"),
                "beta",
                2,
            )
            self.assertEqual([item["tag"] for item in items], ["v2026.38.2", "v2026.38.1"])
            self.assertEqual(items[-1]["title"], "Second")
            self.assertEqual([item["maturity"] for item in items], ["beta", "beta"])

    def test_defaults_to_the_ten_most_recent_notes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            notes = root / "docs" / "releases" / "2026"
            notes.mkdir(parents=True)
            for revision in range(1, 13):
                tag = f"v2026.38.{revision}"
                (notes / f"{tag}.md").write_text(f"# {tag} — Release {revision}\n", encoding="utf-8")
            known = self.maturities(*(f"v2026.38.{revision}" for revision in range(1, 12)))
            items = MODULE.build(root, "v2026.38.12", known, "beta")
            self.assertEqual(len(items), 10)
            self.assertEqual(items[0]["tag"], "v2026.38.12")
            self.assertEqual(items[-1]["tag"], "v2026.38.3")

    def test_requires_the_target_note(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "docs" / "releases" / "2026").mkdir(parents=True)
            with self.assertRaisesRegex(ValueError, "does not contain target"):
                MODULE.build(root, "v2026.38", {}, "release")

    def test_rejects_a_non_positive_limit(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(ValueError, "must be positive"):
                MODULE.build(Path(tmp), "v2026.38", {}, "release", 0)

    def test_uses_release_metadata_instead_of_the_version_shape(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            notes = root / "docs" / "releases" / "2026"
            notes.mkdir(parents=True)
            for tag in ("v2026.38", "v2026.38.1", "v2026.38.2"):
                (notes / f"{tag}.md").write_text(f"# {tag}\n", encoding="utf-8")
            items = MODULE.build(
                root,
                "v2026.38.2",
                {"v2026.38": "beta", "v2026.38.1": "release"},
                "release",
            )
            self.assertEqual(
                [(item["tag"], item["maturity"]) for item in items],
                [("v2026.38.2", "release"), ("v2026.38.1", "release"), ("v2026.38", "beta")],
            )

    def test_reads_only_published_maturity_records(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "releases.json"
            path.write_text(
                '[{"tagName":"v2026.38","isDraft":false,"isPrerelease":false},'
                '{"tagName":"v2026.38.1","isDraft":false,"isPrerelease":true},'
                '{"tagName":"v2026.38.2","isDraft":true,"isPrerelease":false}]',
                encoding="utf-8",
            )
            self.assertEqual(
                MODULE.load_maturities(path),
                {"v2026.38": "release", "v2026.38.1": "beta"},
            )

    def test_skips_older_notes_without_a_published_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            notes = root / "docs" / "releases" / "2026"
            notes.mkdir(parents=True)
            for tag in ("v2026.38", "v2026.38.1", "v2026.38.2"):
                (notes / f"{tag}.md").write_text(f"# {tag}\n", encoding="utf-8")
            items = MODULE.build(root, "v2026.38.2", {"v2026.38": "release"}, "beta")
            self.assertEqual(
                [(item["tag"], item["maturity"]) for item in items],
                [("v2026.38.2", "beta"), ("v2026.38", "release")],
            )


if __name__ == "__main__":
    unittest.main()
