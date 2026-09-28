import unittest

from release_notes import validate_bilingual_notes


class ReleaseNotesTest(unittest.TestCase):
    def test_accepts_one_nonempty_section_per_supported_language(self):
        validate_bilingual_notes("<zh-CN>\n中文说明\n</zh-CN>\n<en-US>\nEnglish notes\n</en-US>")

    def test_rejects_a_missing_language(self):
        with self.assertRaisesRegex(ValueError, "zh-CN"):
            validate_bilingual_notes("<en-US>\nEnglish notes\n</en-US>")

    def test_rejects_empty_or_duplicate_sections(self):
        with self.assertRaises(ValueError):
            validate_bilingual_notes("<zh-CN> </zh-CN><en-US>English</en-US>")
        with self.assertRaises(ValueError):
            validate_bilingual_notes("<zh-CN>一</zh-CN><zh-CN>二</zh-CN><en-US>English</en-US>")


if __name__ == "__main__":
    unittest.main()
