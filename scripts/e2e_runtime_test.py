"""Regression checks for the container-backed end-to-end harness."""
import base64
import hashlib
import hmac
import subprocess
import unittest
from unittest.mock import patch, MagicMock

from e2e_runtime import (
    check_spa, container_secret_key, owner_token, parse_secret_key, restart_container, unused_totp_step,
)


class RuntimeTests(unittest.TestCase):
    def test_totp_before_and_after_boundary(self):
        self.assertEqual(unused_totp_step({100}, now=3029), 101)
        self.assertEqual(unused_totp_step({100}, now=3030), 101)


    @patch('e2e_runtime.urllib.request.urlopen')
    @patch('e2e_runtime.subprocess.run')
    def test_restart_waits_for_container_readiness(self, run, urlopen):
        restart_container('rp-smoke', 'http://localhost:18790')
        run.assert_called_once_with(['docker', 'restart', 'rp-smoke'], check=True, timeout=60)
        urlopen.assert_called_once_with('http://localhost:18790/healthz', timeout=2)

    @patch('e2e_runtime.urllib.request.urlopen')
    def test_accepts_shell_and_built_assets(self, urlopen):
        responses = []
        for body, content_type in [
            (b'<div id="root"></div><script src="/assets/app.js"></script>'
             b'<link rel="stylesheet" href="/assets/app.css">', 'text/html'),
            (b'console.log("loaded")', 'text/javascript'),
            (b'body { color: black; }', 'text/css'),
        ]:
            response = MagicMock()
            response.__enter__.return_value = response
            response.read.return_value = body
            response.headers.get_content_type.return_value = content_type
            responses.append(response)
        urlopen.side_effect = responses
        check_spa('http://localhost:18790')
        self.assertEqual(urlopen.call_count, 3)

    @patch('e2e_runtime.time.sleep')
    @patch('e2e_runtime.urllib.request.urlopen')
    @patch('e2e_runtime.subprocess.run')
    def test_restart_retries_readiness(self, run, urlopen, sleep):
        from urllib.error import URLError
        urlopen.side_effect = [URLError('starting'), MagicMock()]
        restart_container('rp-smoke', 'http://localhost:18790')
        self.assertEqual(urlopen.call_count, 2)
        sleep.assert_called_once_with(1)

    @patch('e2e_runtime.urllib.request.urlopen')
    def test_rejects_html_fallback_for_missing_javascript(self, urlopen):
        page = MagicMock()
        page.read.return_value = b'<div id="root"></div><script src="/assets/app.js"></script>'
        page.headers.get_content_type.return_value = 'text/html'
        urlopen.return_value.__enter__.return_value = page
        with self.assertRaisesRegex(RuntimeError, 'content type'):
            check_spa('http://localhost:18790')

    @patch('e2e_runtime.urllib.request.urlopen')
    def test_rejects_placeholder_page(self, urlopen):
        page = MagicMock()
        page.read.return_value = b'frontend not built'
        urlopen.return_value.__enter__.return_value = page
        with self.assertRaisesRegex(RuntimeError, 'root'):
            check_spa('http://localhost:18790')


class OwnerTokenTests(unittest.TestCase):
    def test_token_is_the_signed_message_the_portal_verifies(self):
        key = 'k' * 32
        payload, signature = owner_token(key, 7, 'alice', ttl=60, now=1000).split('.')
        message = base64.urlsafe_b64decode(payload + '=' * (-len(payload) % 4)).decode()
        self.assertEqual(message, 'ot1|7|alice|1060')
        self.assertEqual(signature, hmac.new(key.encode(), message.encode(), hashlib.sha256).hexdigest())

    def test_payload_is_unpadded_url_safe_base64(self):
        # The portal decodes with RawURLEncoding, so padding or the standard alphabet would not verify.
        for user in ('a', 'ab', 'abc', 'a?b>c'):
            payload = owner_token('k' * 32, 1, user, now=0).split('.')[0]
            self.assertNotIn('=', payload)
            self.assertNotIn('+', payload)
            self.assertNotIn('/', payload)

    def test_a_different_key_gives_a_different_signature(self):
        self.assertNotEqual(owner_token('a' * 32, 1, 'u', now=5), owner_token('b' * 32, 1, 'u', now=5))


class SecretKeyTests(unittest.TestCase):
    def test_reads_the_key_and_never_the_rotated_one(self):
        text = 'listen: ":8790"\nsecret_key_previous: "old-key"\nsecret_key: "abc123"   # signing key\n'
        self.assertEqual(parse_secret_key(text), 'abc123')

    def test_accepts_single_quoted_and_bare_values(self):
        self.assertEqual(parse_secret_key("secret_key: 'abc'\n"), 'abc')
        self.assertEqual(parse_secret_key('  secret_key: abc\n'), 'abc')

    def test_a_commented_or_missing_key_is_empty(self):
        self.assertEqual(parse_secret_key('# secret_key: "abc"\nlisten: ":1"\n'), '')
        self.assertEqual(parse_secret_key(''), '')

    @patch('e2e_runtime.subprocess.run')
    def test_container_environment_key_wins(self, run):
        run.return_value = subprocess.CompletedProcess([], 0, stdout='from-env\n', stderr='')
        self.assertEqual(container_secret_key('rp-smoke'), 'from-env')
        run.assert_called_once()
        self.assertEqual(run.call_args.args[0][:3], ['docker', 'exec', 'rp-smoke'])

    @patch('e2e_runtime.subprocess.run')
    def test_container_falls_back_to_its_config_file(self, run):
        run.side_effect = [
            subprocess.CompletedProcess([], 1, stdout='', stderr=''),
            subprocess.CompletedProcess([], 0, stdout='secret_key: "from-file"\n', stderr=''),
        ]
        self.assertEqual(container_secret_key('rp-smoke'), 'from-file')

    @patch('e2e_runtime.subprocess.run')
    def test_container_without_a_readable_key_is_empty(self, run):
        run.return_value = subprocess.CompletedProcess([], 1, stdout='', stderr='boom')
        self.assertEqual(container_secret_key('rp-smoke'), '')


if __name__ == '__main__':
    unittest.main()
