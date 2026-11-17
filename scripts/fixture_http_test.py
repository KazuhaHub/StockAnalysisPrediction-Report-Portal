import unittest

from fixture_http import SAFE_RESPONSE_HEADERS, fixture_cookie, safe_response_header


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

    def test_response_headers_are_allowlisted_and_injection_safe(self):
        self.assertTrue(safe_response_header("Content-Security-Policy", "default-src 'none'"))
        self.assertTrue(safe_response_header("Set-Cookie", "rp_session=abc; Path=/; SameSite=Lax"))
        self.assertFalse(safe_response_header("X-Unreviewed", "value"))
        self.assertFalse(safe_response_header("Content-Type", "text/html\r\nX-Test: yes"))
        self.assertFalse(safe_response_header("Content-Type", "text/html\nX-Test: yes"))
        self.assertIn("Content-Type", SAFE_RESPONSE_HEADERS)
