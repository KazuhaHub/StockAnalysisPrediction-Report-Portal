"""Small HTTP helpers shared by loopback-only browser fixtures."""
import re


COOKIE_NAME = re.compile(r"^[A-Za-z0-9_-]{1,64}$")
COOKIE_VALUE = re.compile(r"^[A-Za-z0-9._~-]{1,512}$")


def fixture_cookie(name, value):
    if not isinstance(name, str) or not isinstance(value, str) or not COOKIE_NAME.fullmatch(name) or not COOKIE_VALUE.fullmatch(value):
        raise ValueError("invalid fixture cookie")
    return f"{name}={value}; Path=/; SameSite=Lax"
