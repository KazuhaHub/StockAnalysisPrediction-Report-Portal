"""Runtime checks shared by the container smoke and functional tests."""
import base64
import hashlib
import hmac
from html.parser import HTMLParser
import re
import subprocess
import time
import urllib.error
import urllib.request


class SPAParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.root = False
        self.assets = {}

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        self.root |= attrs.get('id') == 'root'
        if tag == 'script' and attrs.get('src'):
            self.assets[attrs['src']] = {'text/javascript', 'application/javascript'}
        if tag == 'link' and attrs.get('rel') == 'stylesheet' and attrs.get('href'):
            self.assets[attrs['href']] = {'text/css'}


# Same-origin scripts the shell loads from the site root rather than from the hashed /assets/ bundle.
# theme-boot.js applies the reader's stored theme before the first paint (the CSP forbids inline
# scripts). Anything else outside /assets/ is still a sign of a broken or tampered build.
ROOT_SCRIPTS = {'/theme-boot.js'}


def check_spa(base):
    with urllib.request.urlopen(base + '/', timeout=20) as response:
        parser = SPAParser()
        parser.feed(response.read().decode())
    if not parser.root or not parser.assets:
        raise RuntimeError('SPA shell is missing its root or built assets')
    for path, types in parser.assets.items():
        if not path.startswith('/assets/') and path not in ROOT_SCRIPTS:
            raise RuntimeError(f'Unexpected SPA asset path: {path}')
        with urllib.request.urlopen(base + path, timeout=20) as response:
            if response.headers.get_content_type() not in types:
                raise RuntimeError(f'Wrong content type for SPA asset: {path}')
            if not response.read():
                raise RuntimeError(f'Empty SPA asset: {path}')
    print(f'PASS SPA shell and {len(parser.assets)} built assets')


def restart_container(container, base):
    subprocess.run(['docker', 'restart', container], check=True, timeout=60)
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(base + '/healthz', timeout=2):
                return
        except OSError:
            # A container being restarted resets the socket as often as it refuses the connection.
            # A reset is neither a URLError nor a TimeoutError — both of which are OSErrors — so
            # catching the family covers all three. Missing the reset failed the smoke test every
            # time it reached the restart layer, which is deterministic, not flaky.
            time.sleep(1)
    raise RuntimeError('Container did not become ready after restart')


def unused_totp_step(used, now=None):
    """Use a fresh current or next step within the server's clock-skew window."""
    current = int(time.time() if now is None else now) // 30
    for step in (current, current + 1):
        if step not in used:
            return step
    raise RuntimeError('No unused TOTP step available')


_SECRET_KEY_LINE = re.compile(r"""^\s*secret_key\s*:\s*["']?([^"'#\s]+)""")


def parse_secret_key(config_text):
    """The secret_key value in a config.yaml, or "" when there is none.

    The colon must follow the key name directly, so a secret_key_previous line is never mistaken for
    the live key, and a commented-out line is not read at all.
    """
    for line in config_text.splitlines():
        match = _SECRET_KEY_LINE.match(line)
        if match:
            return match.group(1)
    return ""


def container_secret_key(container):
    """The key a running container signs with: RP_SECRET_KEY when it was given one, otherwise the
    config file its entrypoint generated. "" when neither can be read."""
    env = subprocess.run(['docker', 'exec', container, 'printenv', 'RP_SECRET_KEY'],
                         capture_output=True, text=True, timeout=30)
    if env.returncode == 0 and env.stdout.strip():
        return env.stdout.strip()
    config = subprocess.run(['docker', 'exec', container, 'sh', '-c', 'cat "${RP_CONFIG:-/app/config/config.yaml}"'],
                            capture_output=True, text=True, timeout=30)
    return parse_secret_key(config.stdout) if config.returncode == 0 else ""


def owner_token(secret, ou, user, ttl=3600, now=None):
    """The signed attribution token a run carries to ingest (mintOwnerToken in server.go).

    The portal mints it for a run and a workflow echoes it back as `owner_token`; a set's frozen OU
    comes from this token alone. Building a valid one is the same path a real run takes: nothing the
    ingest verifies is skipped.
    """
    message = "ot1|%d|%s|%d" % (ou, user, int(time.time() if now is None else now) + ttl)
    signature = hmac.new(secret.encode(), message.encode(), hashlib.sha256).hexdigest()
    return base64.urlsafe_b64encode(message.encode()).rstrip(b"=").decode() + "." + signature
