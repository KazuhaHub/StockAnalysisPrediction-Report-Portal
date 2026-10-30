import unittest

from fixture_http import fixture_cookie


class FixtureCookieTest(unittest.TestCase):
    def test_canonical_cookie(self):
        self.assertEqual(
            fixture_cookie("rp_session", "abc_123-xyz"),
            "rp_session=abc_123-xyz; Path=/; SameSite=Lax",
        )

    def test_rejects_header_and_cookie_syntax(self):
        for name, value in (
            ("rp_session\r\nX-Test", "ok"),
            ("rp_session", "ok\r\nX-Test: yes"),
            ("", "ok"),
            ("rp_session", ""),
            ("rp session", "ok"),
            ("rp_session", "ok; Domain=example.com"),
        ):
            with self.subTest(name=name, value=value):
                with self.assertRaises(ValueError):
                    fixture_cookie(name, value)
