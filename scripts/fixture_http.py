"""Small HTTP helpers shared by loopback-only browser fixtures."""
import re


COOKIE_NAME = re.compile(r"^[A-Za-z0-9_-]{1,64}$")
COOKIE_VALUE = re.compile(r"^[A-Za-z0-9._~-]{1,512}$")
RESPONSE_HEADER_VALUE = re.compile(r"^[\x20-\x7e\t]{0,8192}$")

# Only relay headers needed by the loopback fixture. Header names stay fixed;
# values are accepted only when they contain printable HTTP field bytes.
SAFE_RESPONSE_HEADERS = (
    "Accept-Ranges",
    "Cache-Control",
    "Content-Disposition",
    "Content-Range",
    "Content-Security-Policy",
    "Content-Type",
    "Cross-Origin-Opener-Policy",
    "ETag",
    "Last-Modified",
    "Location",
    "Permissions-Policy",
    "Referrer-Policy",
    "Retry-After",
    "Set-Cookie",
    "Vary",
    "X-Content-Type-Options",
)


def fixture_cookie(name, value):
    if not isinstance(name, str) or not isinstance(value, str) or not COOKIE_NAME.fullmatch(name) or not COOKIE_VALUE.fullmatch(value):
        raise ValueError("invalid fixture cookie")
    return f"{name}={value}; Path=/; SameSite=Lax"


def safe_response_header(name, value):
    return name in SAFE_RESPONSE_HEADERS and isinstance(value, str) and RESPONSE_HEADER_VALUE.fullmatch(value) is not None
