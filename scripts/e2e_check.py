#!/usr/bin/env python3
"""End-to-end functional check, driven through the real HTTP surface.

    ./scripts/e2e_check.py <admin-password>
    E2E_BASE=http://localhost:8791 E2E_DB=/path/to/portal.db ./scripts/e2e_check.py <pw>

For Docker, set E2E_CONTAINER to the container name and E2E_DB to its bind-mounted
SQLite file. E2E_ADMIN_PASSWORD can supply the password without a command-line argument.
The caller needs database write access and permission to restart that container.

Layer G mints the signed OU-attribution token a produced report carries, so it needs the portal's
secret_key. A container is asked for it (RP_SECRET_KEY, else its config file); otherwise it is read
from the config.yaml beside the data directory (the parent of E2E_DB's directory). Set
E2E_SECRET_KEY when the server takes its key from somewhere the harness cannot see. Without a key
the access checks fail by name rather than being skipped.

This exists because the Go and vitest suites cannot catch a whole class of defect: they call store
methods directly, so a feature can be fully correct and still be unreachable through the API an
admin actually uses. That is not hypothetical — it is how the OU tree shipped with SetGroupParent
implemented, tested and impossible to invoke, which silently disabled every inherited setting.

Run it against a portal started on a THROWAWAY database: it creates accounts, changes the admin
password, and restarts the server.

Layers under test, in the order they run (E restarts the server, so it runs last):
  A  first-run bootstrap + core portal (must not have regressed)
  B  ADR 0022 — OU tenancy and tree inheritance, account validity
  C  ADR 0024 — report versions, grants, visibility modes, the reader's switcher
  D  ADR 0023 — password change, 2FA, recovery codes, step-up, SSO gating
  F  sign-in protection (captcha) and self-service registration
  G  ADR 0029 and ADR 0039 — the prediction ledger, its admin surface, and who may read the record
     (the three-way OU scope: none, own, all; external protections; the admin access preview)
  E  state survives a restart, and the schema reconcile is idempotent
"""
from e2e_runtime import (
    check_spa, container_secret_key, owner_token, parse_secret_key, restart_container, unused_totp_step,
)

import base64, hashlib, hmac, json, os, sqlite3, struct, subprocess, sys, time, urllib.error, urllib.parse, urllib.request

BASE = os.environ.get("E2E_BASE", "http://localhost:8791")
DB = os.environ.get("E2E_DB", "/tmp/e2e/data/portal.db")
# Resolved from this script's own location, so the harness runs on any checkout rather than only
# on the machine it was written on. E2E_BIN overrides it.
BIN = os.environ.get(
    "E2E_BIN",
    os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "local-build", "report-portal"),
)
# Derived from the database path, so pointing E2E_DB at a different portal restarts THAT one.
# Hardcoding it silently restarted the wrong server and made the restart checks meaningless.
CFG = os.path.dirname(os.path.dirname(DB))

results = []


def check(layer, name, ok, detail=""):
    results.append((layer, name, bool(ok), detail))
    print(f"  {'PASS' if ok else 'FAIL'}  {name}" + (f"   [{detail}]" if detail and not ok else ""))


class Session:
    """A cookie-holding HTTP client; one per portal user."""

    def __init__(self):
        self.cookie = None

    def req(self, method, path, body=None, token=None, headers=None):
        url = BASE + path
        data = json.dumps(body).encode() if body is not None else None
        r = urllib.request.Request(url, data=data, method=method)
        if data is not None:
            r.add_header("Content-Type", "application/json")
        if self.cookie:
            r.add_header("Cookie", self.cookie)
        if token:
            r.add_header("Authorization", "Bearer " + token)
        for k, v in (headers or {}).items():
            r.add_header(k, v)
        try:
            with urllib.request.urlopen(r, timeout=20) as resp:
                sc = resp.getcode()
                raw = resp.read().decode()
                for h, v in resp.getheaders():
                    if h.lower() == "set-cookie" and "rp_session=" in v:
                        self.cookie = v.split(";")[0]
        except urllib.error.HTTPError as e:
            sc, raw = e.code, e.read().decode()
        try:
            return sc, json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            return sc, {"_raw": raw}

    def login(self, u, p):
        return self.req("POST", "/api/login", {"username": u, "password": p})


def db():
    # A timeout, because the server's first-run stock-name import is a long write transaction and a
    # reader that waits forever turns a slow start into a hung test.
    return sqlite3.connect(DB, timeout=20)


def sql(q, *a):
    c = db()
    try:
        cur = c.execute(q, a)
        c.commit()
        return cur.fetchall()
    finally:
        c.close()


def totp_code(secret, offset=0, step=None):
    key = base64.b32decode(secret + "=" * ((8 - len(secret) % 8) % 8))
    counter = int(time.time()) // 30 + offset if step is None else step
    h = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    o = h[19] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def server_secret():
    """The portal's secret_key, or "" when it cannot be found.

    E2E_SECRET_KEY wins, for a server that takes its key from somewhere the harness cannot see. A
    container is asked directly; otherwise the config.yaml beside the data directory is read.
    """
    if os.environ.get("E2E_SECRET_KEY"):
        return os.environ["E2E_SECRET_KEY"]
    if os.environ.get("E2E_CONTAINER"):
        return container_secret_key(os.environ["E2E_CONTAINER"])
    try:
        with open(os.path.join(CFG, "config.yaml"), encoding="utf-8") as f:
            return parse_secret_key(f.read())
    except OSError:
        return ""


def restart():
    """Restart the e2e server only. Targeted by PORT, never `pkill -f report-portal`, which would
    also take down whatever else the developer has running."""
    if os.environ.get("E2E_CONTAINER"):
        restart_container(os.environ["E2E_CONTAINER"], BASE)
        return
    port = BASE.rsplit(":", 1)[1]
    pids = subprocess.run(["lsof", "-ti", f":{port}"], capture_output=True, text=True).stdout.split()
    for pid in pids:
        subprocess.run(["kill", pid], capture_output=True)
    time.sleep(1)
    subprocess.Popen(f"cd {CFG} && RP_CONFIG=./config.yaml {BIN} >> server.log 2>&1", shell=True)
    for _ in range(40):
        time.sleep(0.4)
        try:
            urllib.request.urlopen(BASE + "/healthz", timeout=1)
            return
        except Exception:
            pass
    raise SystemExit("server did not come back")


ADMIN_PW = os.environ.get("E2E_ADMIN_PASSWORD") or sys.argv[1]
if os.environ.get("E2E_CONTAINER"):
    check_spa(BASE)
admin = Session()

# ---------------------------------------------------------------- A. core
print("\nA. First run and the core portal")
sc, me = admin.login("admin", ADMIN_PW)
check("A", "Admin signs in", sc == 200 and me.get("admin") is True, f"{sc} {me}")
check("A", "First-run seed: 27 report types", len(sql("SELECT name FROM type_config")) == 27)
check("A", "First-run seed: the default group", len(sql("SELECT id FROM user_groups WHERE is_default=1")) == 1)
check("A", "First-run seed: the default and manual versions",
      set(sql("SELECT name FROM report_versions")) == {("default",), ("manual",)})

# Tokens are stored hashed (ADR 0019), so the plaintext exists only in the creation response —
# reading the table gives a NULL `token` column. That is the design working; the harness has to
# capture it at mint time like any other client.
sc, mint = admin.req("POST", "/api/admin/tokens", {"name": "e2e", "scope": "all"})
TOKEN = mint.get("token") or mint.get("value") or ""
check("A", "Mint an API token, returned in clear once", sc == 200 and bool(TOKEN), f"{sc} {mint}")

TODAY = sql("SELECT date('now','localtime')")[0][0]
machine = Session()
sc, r = machine.req("POST", "/api/v1/reports", {
    "symbol": "600519", "date": TODAY, "subtype": "估值分析", "title": "茅台估值分析",
    "body_md": "## 内部估值\n| 因子 | 权重 |\n| 护城河 | 0.35 |\nPrompt: 资深分析师…"}, token=TOKEN)
check("A", "Machine ingest: /api/v1/reports", sc == 200 and r.get("ok"), f"{sc} {r}")
RID_INTERNAL = r.get("id")

sc, home = admin.req("GET", "/api/home")
check("A", "Home feed", sc == 200 and (home.get("total", 0) >= 1 or home.get("groups")), f"{sc}")
sc, stock = admin.req("GET", "/api/stock/600519")
check("A", "Stock page", sc == 200 and stock.get("symbol") == "600519", f"{sc}")
sc, syms = admin.req("GET", "/api/symbols?q=600519")
check("A", "Symbol autocomplete", sc == 200, f"{sc}")
sc, md = admin.req("GET", f"/report/{RID_INTERNAL}/md")
# Asserts the CONTENT, not just the status: this replaced a separate /api/repbody check, and the
# export is the path the product actually serves a body through.
check("A", "Report body export", sc == 200 and "护城河" in md.get("_raw", ""), f"{sc}")
sc, ver = admin.req("GET", "/api/version")
check("A", "Version endpoint", sc in (200, 404), f"{sc}")

# ---------------------------------------------------------------- B. ADR 0022
print("\nB. ADR 0022 — OU tenancy, account validity, quotas, run allow-list")
root = sql("SELECT id FROM user_groups WHERE is_default=1")[0][0]
sc, g = admin.req("POST", "/api/admin/groups", {"name": "客户A"})
OU = g.get("id") or sql("SELECT id FROM user_groups WHERE name='客户A'")[0][0]
sc, _ = admin.req("PUT", f"/api/admin/groups/{OU}", {"name": "客户A", "restricted": True, "parent_id": root})
check("B", "Create a restricted OU", sc == 200, f"{sc}")
sc, g2 = admin.req("POST", "/api/admin/groups", {"name": "客户A-子部门"})
SUB = g2.get("id") or sql("SELECT id FROM user_groups WHERE name='客户A-子部门'")[0][0]
admin.req("PUT", f"/api/admin/groups/{SUB}", {"name": "客户A-子部门", "parent_id": OU})
sc, groups = admin.req("GET", "/api/admin/groups")
sub = [x for x in groups.get("groups", []) if x["id"] == SUB]
check("B", "The restricted flag inherits down the OU tree", bool(sub) and sub[0].get("restricted_effective") is True,
      json.dumps(sub, ensure_ascii=False))

# Created through the admin API, not the adduser CLI: the CLI opens the same SQLite file the
# running server holds, and blocks on its write lock.
sc, _ = admin.req("POST", "/api/admin/users",
                  {"username": "ext", "password": "external-pass-1234", "role": "user",
                   "primary_group": OU})
check("B", "Admin creates an account inside the OU", sc == 200, f"{sc}")
ext = Session()
sc, _ = ext.login("ext", "external-pass-1234")
check("B", "The external account signs in", sc == 200, f"{sc}")

sql("UPDATE users SET expires_at=date('now','localtime','-1 day') WHERE username='ext'")
expired = Session()
sc, _ = expired.login("ext", "external-pass-1234")
check("B", "An expired account cannot sign in", sc != 200, f"{sc}")
sc, _ = ext.req("GET", "/api/me")
check("B", "Expiry ends the existing session immediately", sc == 401, f"{sc}")
sql("UPDATE users SET expires_at=NULL WHERE username='ext'")
ext = Session()
ext.login("ext", "external-pass-1234")
sc, _ = ext.req("GET", "/api/me")
check("B", "Clearing the expiry restores access", sc == 200, f"{sc}")

# ---------------------------------------------------------------- C. ADR 0024
print("\nC. ADR 0024 — report versions")
sc, r = machine.req("POST", "/api/v1/reports", {
    "symbol": "600519", "date": TODAY, "subtype": "估值分析", "title": "茅台估值结论",
    "version": "对外版", "body_md": "## 结论\n综合评分 78/100。"}, token=TOKEN)
RID_PUBLIC = r.get("id")
check("C", "A second version with the same code/date/subtype does not overwrite", sc == 200 and RID_PUBLIC != RID_INTERNAL, f"{sc} {r}")
check("C", "Both rows are in the database",
      len(sql("SELECT id FROM reports WHERE symbol='600519' AND rdate=? AND rtype='估值分析'", TODAY)) == 2)

sc, _ = ext.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("C", "External: an ungranted version is unreadable", sc == 404, f"{sc}")
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "External: granted, but not requested by them — unreadable", sc == 404, f"{sc}")

sc, _ = admin.req("POST", "/api/admin/versions", {
    "name": "对外版", "label": "对外版", "ord": 1, "visibility": "owner",
    "grants": [f"g:{OU}"]})
check("C", "Admin saves the version and its grants", sc == 200, f"{sc}")
sql("INSERT OR IGNORE INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?)",
    f"u:ext", TODAY, RID_PUBLIC)
sc, rep = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "External: they requested it — readable", sc == 200, f"{sc}")
if sc == 200:
    md = json.dumps(rep, ensure_ascii=False)
    check("C", "The external reader's body carries no internal content", "护城河" not in md and "Prompt" not in md)
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("C", "External: the internal version stays unreadable", sc == 404, f"{sc}")

sc, sw = ext.req("GET", f"/api/report/{RID_PUBLIC}/versions")
check("C", "The external switcher lists only readable versions", sc == 200 and len(sw.get("versions", [])) == 1, f"{sc} {sw}")
sc, sw = admin.req("GET", f"/api/report/{RID_PUBLIC}/versions")
check("C", "The admin switcher lists both versions, grouped despite different titles",
      sc == 200 and len(sw.get("versions", [])) == 2, f"{sc} {sw}")

sc, lst = ext.req("GET", "/api/home")
blob = json.dumps(lst, ensure_ascii=False)
check("C", "The external home feed shows no internal report", "茅台估值分析" not in blob, blob[:200])

# Group visibility: a colleague in the same OU can see it
admin.req("POST", "/api/admin/users",
          {"username": "ext2", "password": "external-pass-1234", "role": "user",
           "primary_group": OU})
ext2 = Session()
ext2.login("ext2", "external-pass-1234")
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "Owner-only: a colleague cannot read it", sc == 404, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "group", "grants": [f"g:{OU}"]})
sql("INSERT OR IGNORE INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?)",
    f"g:{OU}", TODAY, RID_PUBLIC)
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "Group-visible: the colleague can read it", sc == 200, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": [f"g:{OU}"]})
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "Back to owner-only: the colleague loses access immediately", sc == 404, f"{sc}")

admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": []})
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "After the grant is revoked, they cannot read it either", sc == 404, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": [f"g:{OU}"]})

sc, vs = admin.req("GET", "/api/admin/versions")
names = [v["name"] for v in vs.get("versions", [])]
check("C", "Admin lists the version registry", sc == 200 and "default" in names and "对外版" in names, f"{names}")
sc, _ = admin.req("DELETE", "/api/admin/versions/default")
check("C", "The default version cannot be deleted", sc != 200, f"{sc}")

# ---------------------------------------------------------------- D. ADR 0023
print("\nD. ADR 0023 — passwords, two-factor, step-up, SSO")
sc, provs = Session().req("GET", "/api/sso/providers")
check("D", "With none configured, the SSO list is empty", sc == 200 and provs.get("providers") == [], f"{sc} {provs}")
sc, _ = Session().req("GET", "/api/auth/oidc/nope/start")
check("D", "An unconfigured SSO route is 404", sc == 404, f"{sc}")

sc, _ = ext.req("POST", "/api/me/2fa/setup")
check("D", "Two-factor: refused without step-up", sc == 403, f"{sc}")
sc, _ = ext.req("POST", "/api/me/2fa/setup", headers={"X-Step-Up-Proof": "wrong"})
check("D", "Two-factor: refused with the wrong password", sc == 403, f"{sc}")
sc, setup = ext.req("POST", "/api/me/2fa/setup", headers={"X-Step-Up-Proof": "external-pass-1234"})
check("D", "Two-factor: the right password starts setup", sc == 200 and setup.get("secret"), f"{sc}")
SECRET = setup.get("secret", "")
if SECRET:
    enabled_step = int(time.time()) // 30
    sc, en = ext.req("POST", "/api/me/2fa/enable", {"code": totp_code(SECRET, step=enabled_step)})
    check("D", "Two-factor: confirming enables it and issues recovery codes",
          sc == 200 and len(en.get("recovery_codes", [])) == 10, f"{sc}")
    RECOVERY = en.get("recovery_codes", [])

    leg1 = Session()
    sc, r1 = leg1.login("ext", "external-pass-1234")
    check("D", "Enabled: the password leg issues no session", sc == 200 and r1.get("totp_required") and not r1.get("user"), f"{sc} {r1}")
    pending = r1.get("token")
    sc, _ = leg1.req("GET", "/api/me")
    check("D", "Enabled: the password alone cannot reach the API", sc == 401, f"{sc}")
    # Track the consumed step explicitly: a boundary may pass during password verification.
    login_step = unused_totp_step({enabled_step})
    sc, r2 = leg1.req("POST", "/api/login/2fa", {"token": pending, "code": totp_code(SECRET, step=login_step)})
    check("D", "Second leg: the code completes the sign-in", sc == 200 and r2.get("user") == "ext", f"{sc} {r2}")

    leg2 = Session()
    _, r3 = leg2.login("ext", "external-pass-1234")
    sc, r4 = leg2.req("POST", "/api/login/2fa", {"token": r3.get("token"), "code": RECOVERY[0]})
    check("D", "A recovery code completes the sign-in", sc == 200 and r4.get("user") == "ext", f"{sc}")
    leg3 = Session()
    _, r5 = leg3.login("ext", "external-pass-1234")
    sc, _ = leg3.req("POST", "/api/login/2fa", {"token": r5.get("token"), "code": RECOVERY[0]})
    check("D", "A recovery code cannot be reused", sc != 200, f"{sc}")

    leg4 = Session()
    _, r6 = leg4.login("ext", "external-pass-1234")
    sc, _ = leg4.req("POST", "/api/login/2fa", {"token": r6.get("token"), "code": "not-a-code"})
    check("D", "A wrong code is refused", sc != 200, f"{sc}")
    sc, _ = leg4.req("POST", "/api/login/2fa", {"token": r6.get("token"), "code": totp_code(SECRET, -1)})
    check("D", "The pending token is single-use: one wrong code voids it", sc != 200, f"{sc}")

admin2 = Session()
admin2.login("admin", ADMIN_PW)
sc, _ = admin2.req("POST", "/api/me/password", {"current": "wrong", "new": "a-brand-new-passphrase"})
check("D", "Password change: the current password is required", sc != 200, f"{sc}")
sc, _ = admin2.req("POST", "/api/me/password", {"current": ADMIN_PW, "new": "a-brand-new-passphrase"})
check("D", "Password change: succeeds", sc == 200, f"{sc}")
old = Session()
sc, _ = old.login("admin", ADMIN_PW)
check("D", "The old password stops working after a change", sc != 200, f"{sc}")
sc, _ = admin.req("GET", "/api/me")
check("D", "A password change ends the other sessions", sc == 401, f"{sc}")
admin = Session()
admin.login("admin", "a-brand-new-passphrase")

sc, meJSON = admin.req("GET", "/api/me")
check("D", "/api/me reports the security state",
      all(k in meJSON for k in ("federated", "totp_enabled", "passkeys")), f"{meJSON}")

# ---------------------------------------------------------------- F. captcha + registration
print("\nF. Captcha and self-service registration")
sc, cfg = admin.req("GET", "/api/register/config")
check("F", "Self-service registration is off by default", sc == 200 and cfg.get("enabled") is False, f"{sc} {cfg}")
sc, _ = Session().req("POST", "/api/register",
                      {"email": "x@example.com", "password": "a-long-enough-password"})
check("F", "The registration route is 404 while it is off", sc == 404, f"{sc}")
sc, cap = Session().req("GET", "/api/captcha?ctx=login")
check("F", "The captcha is not required by default", sc == 200 and cap.get("required") is False, f"{sc} {cap}")

sc, _ = admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "image", "login": True, "forgot": True, "register": True,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "",
                     "default_group": "", "expiry_days": ""}})
check("F", "Admin saves the sign-in protection settings", sc == 200, f"{sc}")

sc, cap = Session().req("GET", "/api/captcha?ctx=register")
check("F", "Turning it on issues a captcha image",
      sc == 200 and cap.get("required") is True and str(cap.get("image", "")).startswith("data:image"),
      f"{sc} {list(cap)}")
check("F", "The captcha endpoint does not leak the answer", "answer" not in json.dumps(cap).lower())

for name, path, body in [("sign-in", "/api/login", {"username": "admin", "password": "x"}),
                         ("password reset", "/api/password/forgot", {"account": "admin"}),
                         ("registration", "/api/register", {"email": "n@example.com",
                                                    "password": "a-long-enough-password"})]:
    sc, b = Session().req("POST", path, body)
    check("F", f"{name}: refused without a captcha, and flagged",
          sc == 400 and b.get("captcha_required") is True, f"{sc} {b}")
check("F", "A refused registration leaves no account behind",
      not sql("SELECT username FROM users WHERE username='n@example.com'"))

# A token service configured without a secret: verification must fail closed, not let through
admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "turnstile", "login": False, "forgot": False, "register": True,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "",
                     "default_group": "", "expiry_days": ""}})
sc, b = Session().req("POST", "/api/register",
                      {"email": "closed@example.com", "password": "a-long-enough-password",
                       "captcha_token": "anything"})
check("F", "A misconfigured verifier fails closed", sc == 400, f"{sc} {b}")
check("F", "Failing closed creates no account either",
      not sql("SELECT username FROM users WHERE username='closed@example.com'"))

sc, _ = admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "image", "login": False, "forgot": False, "register": False,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "corp.example",
                     "default_group": "", "expiry_days": ""}})
sc, _ = Session().req("POST", "/api/register",
                      {"email": "outsider@elsewhere.test", "password": "a-long-enough-password"})
check("F", "The domain allow-list refuses a domain outside it", sc == 400, f"{sc}")
sc, b = Session().req("POST", "/api/register",
                      {"email": "newbie@corp.example", "password": "a-long-enough-password"})
check("F", "An allowed domain can register", sc == 200, f"{sc} {b}")
row = sql("SELECT active, COALESCE(restricted,0), COALESCE(group_id,0) FROM users WHERE username='newbie@corp.example'")
check("F", "A registration with no OU: enabled, restricted, no group",
      row == [(1, 1, 0)], f"{row}")

reg = Session()
sc, _ = reg.login("newbie@corp.example", "a-long-enough-password")
check("F", "The registered account can sign in", sc == 200, f"{sc}")
sc, home = reg.req("GET", "/api/home")
blob = json.dumps(home, ensure_ascii=False)
check("F", "The registered account sees no reports", "茅台" not in blob, blob[:160])
sc, _ = reg.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("F", "The registered account cannot read an internal report", sc == 404, f"{sc}")

sc, _ = Session().req("POST", "/api/register",
                      {"email": "newbie@corp.example", "password": "a-long-enough-password"})
check("F", "A duplicate email is refused explicitly", sc == 409, f"{sc}")
sc, _ = Session().req("POST", "/api/register/verify", {"token": "forged"})
check("F", "A forged confirmation token is refused", sc != 200, f"{sc}")

# ---------------------------------------------------------------- G. ADR 0029
print("\nG. Forecast verification (ADR 0029)")

HORIZON = sql("SELECT date(?, '+1 year')", TODAY)[0][0]


def prediction_payload(mutate=None):
    p = {
        "schema": "predictions.v1",
        "emitter": {"app": "e2e", "version": "V0", "calculator": "e2e", "decision_schema": "decision.v3.4"},
        "policy_version": "e2e",
        "probability_method": "approved_policy_default",
        "decision_status": "eligible",
        "decision_quality": "normal",
        "information_cutoff": TODAY + "T15:00:00+08:00",
        "baseline": {"price": "8.52", "as_of": TODAY, "source": "company_quote"},
        "horizon_end": HORIZON,
        "claims": [
            {"key": "stance", "kind": "stance", "stance": "long", "target_weight": "0.1000"},
            {"key": "expected_terminal", "kind": "terminal_value", "price": "9.95", "p_profit": "0.7000"},
            {"key": "scenario.bull", "kind": "scenario", "label": "bull", "price": "14.00", "probability": "0.2500"},
            {"key": "scenario.base", "kind": "scenario", "label": "base", "price": "10.00", "probability": "0.4500"},
            {"key": "scenario.bear", "kind": "scenario", "label": "bear", "price": "6.50", "probability": "0.3000"},
            {"key": "entry_price", "kind": "entry_price", "price": "8.65"},
        ],
    }
    if mutate:
        mutate(p)
    return p


# A report type the ledger covers (a data value, not a label).
PRED_RTYPE = "投资决策建议"


def ingest_prediction(symbol, title, payload=None, owner=None):
    """Ingest a covered report, optionally with predictions and a signed owner token."""
    body = {"symbol": symbol, "date": TODAY, "subtype": PRED_RTYPE, "title": title,
            "body_md": "## e2e 预测\n正文"}
    if payload is not None:
        body["predictions"] = payload
    if owner is not None:
        body["owner_token"] = owner
    return machine.req("POST", "/api/v1/reports", body, token=TOKEN)


sc, r = ingest_prediction("300750", "E2E 有效预测", prediction_payload())
RID_ACCEPTED = r.get("id")
check("G", "A valid payload is accepted", sc == 200 and r.get("predictions_status") == "accepted", f"{sc} {r}")

def drop_bear(p):
    p["claims"] = [c for c in p["claims"] if c.get("label") != "bear"]


sc, r = ingest_prediction("300751", "E2E 无效预测", prediction_payload(drop_bear))
RID_REJECTED = r.get("id")
check("G", "An invalid payload is rejected, and the report still exists",
      sc == 200 and r.get("predictions_status") == "rejected" and bool(RID_REJECTED), f"{sc} {r}")
check("G", "The rejected ingest left an audit row",
      sql("SELECT COUNT(*) FROM audit_log WHERE action='predictions.reject' AND target_id=?",
          str(RID_REJECTED))[0][0] == 1)
check("G", "The rejected ingest still stored the report",
      sql("SELECT COUNT(*) FROM reports WHERE id=?", RID_REJECTED)[0][0] == 1)

sc, r = ingest_prediction("300752", "E2E 无预测")
check("G", "A covered rtype with no predictions is absent",
      sc == 200 and r.get("predictions_status") == "absent", f"{sc} {r}")

sc, sets = machine.req("GET", f"/api/v1/reports/{RID_ACCEPTED}/predictions", token=TOKEN)
accepted_sets = (sets.get("sets") or []) if isinstance(sets, dict) else []
check("G", "The machine route returns the accepted set",
      sc == 200 and len(accepted_sets) >= 1 and accepted_sets[0].get("state") == "accepted",
      f"{sc} {sets}")

sc, groups = admin.req("GET", "/api/admin/forecast/groups")
check("G", "An admin reads the group statistics", sc == 200 and groups.get("ok") is True, f"{sc} {groups}")
sc, _ = ext.req("GET", "/api/admin/forecast/groups")
check("G", "A non-manager is refused the group statistics", sc == 403, f"{sc}")

sc, settings = admin.req("GET", "/api/admin/forecast")
check("G", "The evaluator reads its settings", sc == 200 and settings.get("enabled") is False, f"{sc} {settings}")
bench_before = settings.get("bench")
bench_audit = lambda: sql("SELECT COUNT(*) FROM audit_log WHERE action='forecast.bench'")[0][0]
bench_rows = bench_audit()

# A benchmark change is effective-dated: it schedules for the next cohort and leaves the active
# value alone, so nothing already on the ledger is re-scored.
sc, scheduled = admin.req("POST", "/api/admin/forecast", {"bench_schedule": "sh000300"})
check("G", "A benchmark change is scheduled for the next cohort",
      sc == 200 and bool(scheduled.get("effective_cohort")), f"{sc} {scheduled}")
sc, sched = admin.req("GET", "/api/admin/forecast")
check("G", "The schedule reads back with the active value unchanged",
      sched.get("bench") == bench_before and sched.get("bench_next") == "sh000300", f"{sched}")
check("G", "The schedule is audited", bench_audit() == bench_rows + 1, f"{bench_rows} -> {bench_audit()}")

# A mode-only save touches the switch and nothing else. P9 adds `dry`: it plans and writes nothing,
# and it is the only mode a fresh ledger can reach before the readiness list (which includes P41's
# alert verification) is satisfied.
sc, _ = admin.req("POST", "/api/admin/forecast", {"mode": "dry"})
check("G", "A mode-only save selects dry mode",
      sc == 200 and sql("SELECT v FROM meta WHERE k='forecast_auto'")[0][0] == "dry", f"{sc}")

# Mode 1 is refused while the readiness list has a failing item; the refusal names it.
sc, notready = admin.req("POST", "/api/admin/forecast", {"mode": 1})
check("G", "Mode 1 is refused until the readiness list holds",
      sc == 409 and notready.get("code") == "forecast_not_ready"
      and bool(notready.get("items")), f"{sc} {notready}")

sc, _ = admin.req("POST", "/api/admin/forecast", {"mode": 0})
check("G", "A mode-only save turns settling off again",
      sc == 200 and sql("SELECT v FROM meta WHERE k='forecast_auto'")[0][0] == "0", f"{sc}")
sc, off = admin.req("POST", "/api/admin/forecast/evaluate", {})
check("G", "Evaluate now is refused while the evaluator is off",
      sc == 409 and off.get("code") == "forecast_eval_off", f"{sc} {off}")

# The public status probe answers one key and never the heartbeat.
sc, probe = Session().req("GET", "/healthz/forecast")
check("G", "The evaluator probe is status-only",
      sc == 200 and probe == {"ok": True}, f"{sc} {probe}")

# Cancelling clears the schedule; rescheduling the active value is then a no-op with no audit row.
sc, _ = admin.req("POST", "/api/admin/forecast", {"bench_cancel": True})
cancel_ok = sc == 200
sc, after_cancel = admin.req("GET", "/api/admin/forecast")
rows_after_cancel = bench_audit()
check("G", "Cancel clears the schedule",
      cancel_ok and sc == 200 and not after_cancel.get("bench_next"), f"{after_cancel}")
sc, _ = admin.req("POST", "/api/admin/forecast", {"bench_schedule": bench_before})
sc, noop = admin.req("GET", "/api/admin/forecast")
check("G", "Rescheduling the active benchmark is a no-op",
      sc == 200 and bench_audit() == rows_after_cancel and not noop.get("bench_next"), f"{sc} {noop}")

# The ledger start (P5): a bounded, write-once instant. The e2e portal is a throwaway database, so
# setting it here is safe. Preview first (the GET carries the bound and the candidates), then save,
# then a second save must be refused as a conflict.
pop = settings.get("population") or {}
check("G", "The ledger start is unset on a fresh portal", pop.get("set") is False, f"{pop}")
sc, saved = admin.req("POST", "/api/admin/forecast", {"population_from": "2000-01-01T00:00:00Z"})
check("G", "An early, bounded ledger start saves", sc == 200, f"{sc} {saved}")
sc, again = admin.req("POST", "/api/admin/forecast", {"population_from": "2000-01-02T00:00:00Z"})
check("G", "A second ledger-start save is refused (write-once)", sc == 409, f"{sc} {again}")
sc, settings2 = admin.req("GET", "/api/admin/forecast")
check("G", "The ledger start reads back as set",
      (settings2.get("population") or {}).get("set") is True, f"{settings2.get('population')}")

# ---------------------------------------------------------------- G, continued: who may read the record
# ADR 0039. The record is a reader feature gated by the OU scope: none (no access), own (only the
# sets the account's OU subtree produced) or all (the whole ledger), with an external account getting
# the whole ledger minus its volumes. These checks use the cookie routes the SPA uses, against
# attribution created through the real ingest path (a signed owner token on a produced report).
# They run after the ledger start is set, so the record is a live one.
print("\nG. Forecast verification — who may read the record (ADR 0039)")

FC_PW = "forecast-pass-123456"


def short(v, n=240):
    return json.dumps(v, ensure_ascii=False)[:n]


def code_of(body):
    return body.get("code") if isinstance(body, dict) else None


def make_ou(name, parent=None):
    body = {"name": name}
    if parent is not None:
        body["parent_id"] = parent
    sc, g = admin.req("POST", "/api/admin/groups", body)
    return g.get("id") if sc == 200 else None


def put_ou(ou, name, **fields):
    """PUT replaces the OU's whole configuration, so a call names only what the check is about."""
    return admin.req("PUT", f"/api/admin/groups/{ou}", {"name": name, **fields})


def make_member(username, ou):
    sc, _ = admin.req("POST", "/api/admin/users",
                      {"username": username, "password": FC_PW, "role": "user", "primary_group": ou})
    session = Session()
    sc_login, _ = session.login(username, FC_PW)
    return session if sc == 200 and sc_login == 200 else None


def reports_in(resp):
    """The record's row count: the coverage `reports` summed over the monthly groups."""
    return sum((g.get("coverage") or {}).get("reports", 0) for g in (resp.get("groups") or []))


def scope_counts(overview):
    c = overview.get("counts") or {}
    return c.get("all"), c.get("own"), c.get("none")


def set_ids(*ous):
    marks = ",".join("?" * len(ous))
    return sorted(r[0] for r in sql(
        f"SELECT id FROM prediction_sets WHERE rtype=? AND state='accepted' AND attributed_ou IN ({marks})",
        PRED_RTYPE, *ous))


def forecast_access_checks():
    secret = server_secret()
    check("G", "The OU-attribution key is available to the harness", bool(secret),
          "set E2E_SECRET_KEY, or run beside the portal's config.yaml")
    if not secret:
        return
    ous = {"internal": make_ou("fc-internal"), "own": make_ou("fc-own"), "other": make_ou("fc-other"),
           "empty": make_ou("fc-empty"), "ext": make_ou("fc-ext")}
    ous["sub"] = make_ou("fc-own-sub", parent=ous["own"])
    ok = all(ous.values())
    check("G", "Fixture: six OUs, one of them a child of fc-own", ok, short(ous))
    if not ok:
        return
    INT, OWN, SUB, OTHER, EMPTY, EXT = (ous[k] for k in ("internal", "own", "sub", "other", "empty", "ext"))

    # Scopes: fc-internal is left alone (NULL, so it inherits the Default OU's none); fc-own-sub is
    # a child with no setting of its own, so it must inherit fc-own's own.
    saved = [put_ou(OWN, "fc-own", forecast_scope="own"), put_ou(OTHER, "fc-other", forecast_scope="own"),
             put_ou(EMPTY, "fc-empty", forecast_scope="own"),
             put_ou(EXT, "fc-ext", restricted=True, forecast_scope="all")]
    check("G", "Fixture: the four scope settings save", all(sc == 200 for sc, _ in saved), short(saved))
    sc, tree = admin.req("GET", "/api/admin/groups")
    rows = {g["id"]: g for g in tree.get("groups", [])}
    check("G", "The admin tree reports the stored, effective and source scope",
          rows.get(OWN, {}).get("forecast_scope") == "own"
          and rows.get(SUB, {}).get("forecast_scope") is None
          and rows.get(SUB, {}).get("forecast_scope_effective") == "own"
          and rows.get(SUB, {}).get("forecast_scope_source") == OWN
          and rows.get(INT, {}).get("forecast_scope") is None
          and rows.get(INT, {}).get("forecast_scope_effective") == "none",
          short({k: rows.get(k) for k in (OWN, SUB, INT)}, 600))

    members = {name: make_member(name, ou) for name, ou in
               (("fcint", INT), ("fcown", OWN), ("fcsub", SUB), ("fcother", OTHER), ("fcempty", EMPTY),
                ("fcext", EXT))}
    check("G", "Fixture: six members sign in", all(members.values()), short({k: bool(v) for k, v in members.items()}))
    if not all(members.values()):
        return
    fcint, fcown, fcsub, fcother, fcempty, fcext = (members[k] for k in
                                                    ("fcint", "fcown", "fcsub", "fcother", "fcempty", "fcext"))

    # Attribution through the real ingest path. fc-own's subtree holds three sets (two in fc-own, one
    # in its child) and a sibling OU holds one more, so a wrong subtree, a missing descendant and a
    # leak across siblings all change a number the checks compare.
    plan = [("300761", OWN, "fcown"), ("300762", SUB, "fcsub"), ("300763", OTHER, "fcother"),
            ("300764", OWN, "fcown")]
    ingested = []
    for symbol, ou, who in plan:
        sc, r = ingest_prediction(symbol, f"FC {symbol}", prediction_payload(), owner_token(secret, ou, who))
        stored = sql("SELECT attributed_ou FROM prediction_sets WHERE report_id=?", r.get("id"))
        ingested.append(sc == 200 and r.get("predictions_status") == "accepted" and stored == [(ou,)])
    check("G", "Fixture: four sets are accepted, each frozen to the OU of its signed owner token",
          all(ingested), short(ingested))
    check("G", "A forged owner token attributes nothing",
          ingest_prediction("300766", "FC forged", prediction_payload(),
                            owner_token("not-the-portal-key", OWN, "fcown"))[0] == 200
          and sql("SELECT attributed_ou FROM prediction_sets WHERE symbol='300766'") == [(None,)])

    # ---- anonymous: 401 on every reader route, and on the admin preview
    anon = Session()
    for label, path in (("the record", "/api/forecast/groups"),
                        ("the members drawer", "/api/forecast/groups/members?label=x&checkpoint=d5"),
                        ("a reading page's predictions", f"/api/report/{RID_ACCEPTED}/predictions"),
                        ("the v1 predictions read", f"/api/v1/reports/{RID_ACCEPTED}/predictions"),
                        ("the access overview", "/api/admin/forecast/access")):
        sc, _ = anon.req("GET", path)
        check("G", f"Anonymous: {label} answers 401", sc == 401, f"{sc}")

    # ---- an internal member of an OU at none (here inherited from the Default OU)
    sc, me = fcint.req("GET", "/api/me")
    check("G", "None: /api/me reports scope none for navigation",
          sc == 200 and (me.get("forecast") or {}).get("scope") == "none", short(me.get("forecast")))
    for label, path in (("the record", "/api/forecast/groups"),
                        ("the members drawer", "/api/forecast/groups/members?label=x&checkpoint=d5"),
                        ("a reading page's predictions", f"/api/report/{RID_ACCEPTED}/predictions"),
                        ("the v1 predictions read by cookie", f"/api/v1/reports/{RID_ACCEPTED}/predictions")):
        sc, body = fcint.req("GET", path)
        check("G", f"None: {label} is refused with forecast_unavailable",
              sc == 403 and code_of(body) == "forecast_unavailable", f"{sc} {short(body)}")
    for label, path in (("the group statistics", "/api/admin/forecast/groups"),
                        ("the settings", "/api/admin/forecast"),
                        ("the access overview", "/api/admin/forecast/access")):
        sc, _ = fcint.req("GET", path)
        check("G", f"A reader never reaches the admin routes: {label}", sc == 403, f"{sc}")

    # ---- the access preview: read-only, and equal to what the save then does
    active = sql("SELECT COUNT(*) FROM users WHERE COALESCE(active,1)=1 AND "
                 "(COALESCE(expires_at,'')='' OR expires_at>=date('now','localtime'))")[0][0]
    group_changes = lambda: sql("SELECT COUNT(*) FROM audit_log WHERE action='group.change'")[0][0]
    sc, before = admin.req("GET", "/api/admin/forecast/access")
    check("G", "The access overview counts every active account exactly once",
          sc == 200 and sum(c or 0 for c in scope_counts(before)) == active,
          f"{sc} {scope_counts(before)} accounts={active}")
    audit_rows = group_changes()
    sc, prev_int = admin.req("GET", f"/api/admin/forecast/access?ou={INT}&scope=all")
    b_all, b_own, b_none = scope_counts(before)
    p_all, p_own, p_none = scope_counts(prev_int)
    check("G", "Preview: raising fc-internal to all moves exactly its one member from none to all",
          sc == 200 and prev_int.get("preview") is True and prev_int.get("affected") == 1
          and (p_all, p_own, p_none) == (b_all + 1, b_own, b_none - 1),
          f"{sc} affected={prev_int.get('affected')} before={scope_counts(before)} after={scope_counts(prev_int)}")
    sc, prev_own = admin.req("GET", f"/api/admin/forecast/access?ou={OWN}&scope=all")
    o_all, o_own, o_none = scope_counts(prev_own)
    check("G", "Preview: raising fc-own to all carries the inheriting child with it (two own readers become all)",
          sc == 200 and prev_own.get("affected") == 2 and (o_all, o_own, o_none) == (b_all + 2, b_own - 2, b_none),
          f"{sc} affected={prev_own.get('affected')} before={scope_counts(before)} after={scope_counts(prev_own)}")
    sc, bad_prev = admin.req("GET", f"/api/admin/forecast/access?ou={INT}&scope=everyone")
    check("G", "Preview: an unknown scope is refused with forecast_bad_scope",
          sc == 400 and code_of(bad_prev) == "forecast_bad_scope", f"{sc} {short(bad_prev)}")
    sc, tree = admin.req("GET", "/api/admin/groups")
    stored_after_previews = {g["id"]: g.get("forecast_scope") for g in tree.get("groups", [])}
    sc_int, _ = fcint.req("GET", "/api/forecast/groups")
    check("G", "Previews write nothing: no scope moved, no audit row, access unchanged",
          stored_after_previews.get(INT) is None and stored_after_previews.get(OWN) == "own"
          and group_changes() == audit_rows and sc_int == 403,
          f"{short(stored_after_previews)} audit {audit_rows}->{group_changes()} member={sc_int}")

    # ---- raising the member's OU to all (the spec's flip from 403 to 200)
    sc, _ = put_ou(INT, "fc-internal", forecast_scope="all")
    check("G", "Raising the OU to all saves", sc == 200, f"{sc}")
    sc, after = admin.req("GET", "/api/admin/forecast/access")
    check("G", "The access preview's counts are exactly the counts after the save",
          sc == 200 and scope_counts(after) == scope_counts(prev_int), f"{scope_counts(prev_int)} vs {scope_counts(after)}")
    ou_row = next((g for g in after.get("groups", []) if g.get("id") == INT), {})
    check("G", "The overview's per-OU row names the effective scope, its source and the member",
          ou_row.get("forecast_scope") == "all" and ou_row.get("forecast_scope_effective") == "all"
          and ou_row.get("forecast_scope_source") == INT and (ou_row.get("counts") or {}).get("all") == 1,
          short(ou_row))
    audit = sql("SELECT detail FROM audit_log WHERE action='group.change' AND target_id=? ORDER BY id DESC LIMIT 1",
                str(INT))
    detail = json.loads(audit[0][0]) if audit and audit[0][0] else {}
    check("G", "The save is audited with the scope it moved from and to",
          detail.get("forecast_scope") == {"from": None, "to": "all"}, short(detail))

    sc, me = fcint.req("GET", "/api/me")
    check("G", "All: /api/me reports scope all for navigation",
          sc == 200 and (me.get("forecast") or {}).get("scope") == "all", short(me.get("forecast")))
    sc, rec = fcint.req("GET", "/api/forecast/groups")
    total_rows = sql("SELECT COUNT(*) FROM prediction_sets WHERE rtype=?", PRED_RTYPE)[0][0]
    check("G", "All: the same member now reads the record, and the record is the whole ledger",
          sc == 200 and rec.get("ok") is True and len(rec.get("groups") or []) >= 1
          and reports_in(rec) == total_rows and total_rows >= 8,
          f"{sc} rows={reports_in(rec)} ledger={total_rows}")
    check("G", "All: an internal reader's groups carry coverage and capabilities, but never `unrecorded`",
          all("coverage" in g for g in rec.get("groups", [])) and "unrecorded" not in rec
          and (rec.get("capabilities") or {}).get("custom") is True, short(rec.get("capabilities")))
    sc, adm = admin.req("GET", "/api/admin/forecast/groups")
    check("G", "The admin alias still carries `unrecorded` (until the management page is split)",
          sc == 200 and "unrecorded" in adm and reports_in(adm) == reports_in(rec), f"{sc} {short(list(adm))}")
    sc, preds = fcint.req("GET", f"/api/report/{RID_ACCEPTED}/predictions")
    pred_sets = preds.get("sets") or []
    check("G", "All: the reading page's predictions answer 200 with the accepted set, its membership and no payload",
          sc == 200 and len(pred_sets) >= 1 and all(s.get("state") == "accepted" for s in pred_sets)
          and all(s.get("membership") for s in pred_sets) and all(not s.get("payload") for s in pred_sets),
          f"{sc} {short(preds)}")
    sc, rejected = fcint.req("GET", f"/api/report/{RID_REJECTED}/predictions")
    check("G", "All: a reader never sees a rejected set",
          sc == 200 and (rejected.get("sets") or []) == [], f"{sc} {short(rejected)}")
    sc, v1 = fcint.req("GET", f"/api/v1/reports/{RID_ACCEPTED}/predictions")
    check("G", "All: the v1 predictions read by cookie answers 200", sc == 200, f"{sc} {short(v1)}")
    # The range is derived from the ledger's own dates: made_on is a Shanghai civil date, which can
    # be a day ahead of this machine's TODAY.
    last_day = sql("SELECT MAX(made_on) FROM prediction_sets WHERE rtype=?", PRED_RTYPE)[0][0]
    custom = "/api/forecast/groups?" + urllib.parse.urlencode(
        {"mode": "custom", "from": sql("SELECT date(?, '-30 day')", last_day)[0][0], "to": last_day})
    sc, cust = fcint.req("GET", custom)
    check("G", "All: an internal reader may use a custom range, over the whole ledger",
          sc == 200 and cust.get("mode") == "custom" and cust.get("official") is False
          and reports_in(cust) == total_rows, f"{sc} rows={reports_in(cust)} ledger={total_rows} {short(cust, 120)}")

    # ---- a PUT without the field leaves the stored scope alone
    sc, _ = put_ou(INT, "fc-internal", description="renamed by the e2e check")
    sc2, tree = admin.req("GET", "/api/admin/groups")
    row = next((g for g in tree.get("groups", []) if g["id"] == INT), {})
    audit = sql("SELECT detail FROM audit_log WHERE action='group.change' AND target_id=? ORDER BY id DESC LIMIT 1",
                str(INT))
    detail = json.loads(audit[0][0]) if audit and audit[0][0] else {}
    sc3, _ = fcint.req("GET", "/api/forecast/groups")
    check("G", "A PUT without forecast_scope leaves it unchanged, writes no scope audit and keeps access",
          sc == 200 and row.get("forecast_scope") == "all" and "forecast_scope" not in detail and sc3 == 200,
          f"{sc} stored={row.get('forecast_scope')} detail={short(detail)} member={sc3}")
    sc, bad = put_ou(INT, "fc-internal", forecast_scope="everyone")
    sc2, tree = admin.req("GET", "/api/admin/groups")
    row = next((g for g in tree.get("groups", []) if g["id"] == INT), {})
    check("G", "An unknown scope is refused with forecast_bad_scope and changes nothing",
          sc == 400 and code_of(bad) == "forecast_bad_scope" and row.get("forecast_scope") == "all",
          f"{sc} {short(bad)} stored={row.get('forecast_scope')}")
    # An explicit null is a different statement from an absent field: it chooses inheritance, and
    # fc-internal's parent is the Default OU, which stores none.
    sc, _ = put_ou(INT, "fc-internal", forecast_scope=None)
    sc2, tree = admin.req("GET", "/api/admin/groups")
    row = next((g for g in tree.get("groups", []) if g["id"] == INT), {})
    sc3, _ = fcint.req("GET", "/api/forecast/groups")
    check("G", "An explicit null chooses inheritance: the OU inherits none and the member is refused again",
          sc == 200 and row.get("forecast_scope") is None and row.get("forecast_scope_effective") == "none"
          and sc3 == 403, f"{sc} stored={row.get('forecast_scope')} effective={row.get('forecast_scope_effective')} member={sc3}")

    # ---- own: the subtree population, by frozen attribution
    own_sets = set_ids(OWN, SUB)
    expect = {"fcown": len(own_sets), "fcsub": len(set_ids(SUB)), "fcother": len(set_ids(OTHER)), "fcempty": 0}
    got = {}
    bodies = {}
    for name, session in (("fcown", fcown), ("fcsub", fcsub), ("fcother", fcother), ("fcempty", fcempty)):
        sc, body = session.req("GET", "/api/forecast/groups")
        bodies[name] = body
        got[name] = reports_in(body) if sc == 200 else f"HTTP {sc}"
    check("G", "Own: each reader's record is exactly its OU subtree's sets (3 / 1 / 1 / 0), not the ledger",
          got == expect and expect == {"fcown": 3, "fcsub": 1, "fcother": 1, "fcempty": 0},
          f"got={got} expected={expect} ledger={total_rows}")
    check("G", "Own: an OU with no attributed sets reads an empty record, never the whole ledger",
          not (bodies["fcempty"].get("groups") or []), short(bodies["fcempty"].get("groups")))
    check("G", "Own: the reader still has coverage and no `unrecorded`",
          all("coverage" in g for g in bodies["fcown"].get("groups", [])) and bodies["fcown"].get("groups")
          and "unrecorded" not in bodies["fcown"], short(bodies["fcown"].get("groups"), 160))
    label = ((bodies["fcown"].get("groups") or [{}])[0]).get("label", "")
    query = urllib.parse.urlencode({"label": label, "checkpoint": "d5", "population": "all"})
    sc, drawer = fcown.req("GET", "/api/forecast/groups/members?" + query)
    drawer_ids = sorted(m.get("set_id") for m in drawer.get("members", []))
    check("G", "Own: the members drawer lists exactly the subtree's attributed sets, with its totals",
          sc == 200 and drawer_ids == own_sets and len(own_sets) == 3 and "total" in drawer and "n" in drawer,
          f"{sc} drawer={drawer_ids} subtree={own_sets} keys={sorted(drawer)}")
    sc, card = fcown.req("GET", f"/api/report/{RID_ACCEPTED}/predictions")
    check("G", "Own: the reading page's predictions answer 200 for any readable report, with no membership badge "
               "(the badge refers to the all record)",
          sc == 200 and len(card.get("sets") or []) >= 1 and all(not s.get("membership") for s in card["sets"]),
          f"{sc} {short(card)}")
    sc, cust = fcown.req("GET", custom)
    check("G", "Own: an internal reader may use a custom range, and it stays inside the subtree",
          sc == 200 and reports_in(cust) == len(own_sets), f"{sc} rows={reports_in(cust)} subtree={len(own_sets)}")

    # ---- an external member at all: the whole ledger, without its volumes, monthly only
    # Per-cell volumes (n, totals) exist only on a mature cell, and a mature cell needs a settled
    # outcome the e2e cannot produce (the evaluator is off); TestForecastAccessMatrix pins that path.
    sc, ext_rec = fcext.req("GET", "/api/forecast/groups")
    groups = ext_rec.get("groups") or []
    check("G", "External all: the record is readable and non-empty",
          sc == 200 and ext_rec.get("ok") is True and len(groups) >= 1, f"{sc} {short(ext_rec)}")
    check("G", "External all: no group carries coverage, and no site-wide metadata or `unrecorded` is sent",
          all("coverage" not in g for g in groups)
          and not any(k in ext_rec for k in ("unrecorded", "summary", "capabilities", "ledger_start", "limits")),
          short(sorted(ext_rec)))
    sc, ext_custom = fcext.req("GET", custom)
    check("G", "External all: a custom range is refused with forecast_custom_not_allowed",
          sc == 403 and code_of(ext_custom) == "forecast_custom_not_allowed", f"{sc} {short(ext_custom)}")
    sc, me = fcext.req("GET", "/api/me")
    check("G", "External all: /api/me marks the account external for navigation",
          sc == 200 and (me.get("forecast") or {}).get("scope") == "all"
          and (me.get("forecast") or {}).get("external") is True, short(me.get("forecast")))
    ext_label = (groups[0] if groups else {}).get("label", "")
    query = urllib.parse.urlencode({"label": ext_label, "checkpoint": "d5"})
    sc, ext_drawer = fcext.req("GET", "/api/forecast/groups/members?" + query)
    check("G", "External all: the members drawer carries no totals or per-status counts",
          sc == 200 and not any(k in ext_drawer for k in ("n", "total", "resolved", "refused", "corp_action")),
          f"{sc} {short(ext_drawer)}")
    sc, ext_card = fcext.req("GET", f"/api/report/{RID_ACCEPTED}/predictions")
    check("G", "External all: another OU's report is not disclosed through the reading page (404, not its sets)",
          sc == 404, f"{sc} {short(ext_card)}")
    sc, ext_adm = fcext.req("GET", "/api/admin/forecast/groups")
    check("G", "External all: the admin alias stays closed", sc == 403, f"{sc}")


forecast_access_checks()

# ---------------------------------------------------------------- restart
print("\nE. State survives a restart")
restart()
admin = Session()
sc, _ = admin.login("admin", "a-brand-new-passphrase")
check("E", "Sign-in works after the restart", sc == 200, f"{sc}")
sc, vs = admin.req("GET", "/api/admin/versions")
check("E", "Versions and grants survive the restart",
      sc == 200 and any(v["name"] == "对外版" and v["grants"] for v in vs.get("versions", [])), f"{sc}")
check("E", "The restart does not rebuild the unique index",
      "version" in sql("SELECT sql FROM sqlite_master WHERE name='idx_reports_ident'")[0][0])

# ---------------------------------------------------------------- report
print("\n" + "=" * 64)
bad = [r for r in results if not r[2]]
by = {}
for layer, _, ok, _ in results:
    a, b = by.get(layer, (0, 0))
    by[layer] = (a + (1 if ok else 0), b + 1)
for layer in sorted(by):
    p, n = by[layer]
    print(f"  {layer}: {p}/{n}")
print(f"\n  {len(results) - len(bad)}/{len(results)} passed")
if bad:
    print("\n  failures:")
    for layer, name, _, detail in bad:
        print(f"    [{layer}] {name}   {detail}")
sys.exit(1 if bad else 0)
