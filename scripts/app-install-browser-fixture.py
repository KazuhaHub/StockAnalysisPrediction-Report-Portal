"""Serve the real installation page against an owned disposable Portal store.

Loopback only. Uses synthetic administrator/publisher/session data; no production
database, credentials, runtime or server configuration. Stop with Ctrl-C.
"""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
from urllib.parse import urlsplit

PORTAL = Path(__file__).resolve().parents[1]
WEB = PORTAL / "web"

with tempfile.TemporaryDirectory(prefix="portal-install-browser-") as directory:
    target = Path(directory)
    (target / "node_modules").symlink_to(WEB / "node_modules", target_is_directory=True)
    source = target / "host.tsx"
    source.write_text("import " + json.dumps(str(WEB / "src/index.css")) + ";\n" + """import React from 'react';import {createRoot} from 'react-dom/client';
import {BrowserRouter,Routes,Route} from 'react-router';
import {ConfigProvider,App,theme} from 'antd';
import {AuthProvider,useAuth} from """ + json.dumps(str(WEB / "src/auth.tsx")) + ";\nimport {PrefsProvider,usePrefs} from " + json.dumps(str(WEB / "src/prefs.tsx")) + ";\nimport AppsAdminPage from " + json.dumps(str(WEB / "src/pages/manage/AppsAdminPage.tsx")) + """;
function Host(){const prefs=usePrefs();const auth=useAuth();return <ConfigProvider locale={prefs.antd} theme={{algorithm:prefs.dark?theme.darkAlgorithm:theme.defaultAlgorithm}}><App><main style={{minHeight:'100vh',padding:24,background:prefs.dark?'#000':'#fff',color:prefs.dark?'#fff':'#000'}}><nav><button onClick={()=>prefs.setLang('en-US')}>English</button><button onClick={()=>prefs.setLang('zh-CN')}>Simplified Chinese</button><button onClick={()=>prefs.setLang('zh-TW')}>Traditional Chinese</button><button onClick={()=>prefs.setMode(prefs.dark?'light':'dark')}>Toggle theme</button></nav>{!auth.loading&&auth.admin&&<BrowserRouter><Routes><Route path='/manage/apps' element={<AppsAdminPage/>}/><Route path='/apps' element={<p>Application hub destination</p>}/></Routes></BrowserRouter>}</main></App></ConfigProvider>}
createRoot(document.getElementById('host')!).render(<PrefsProvider><AuthProvider><Host/></AuthProvider></PrefsProvider>);
""")
    config = target / "vite.config.mjs"
    aliases = {name: str(WEB / "node_modules" / name) for name in ("react", "react-dom", "antd", "react-router")}
    config.write_text("import {defineConfig} from 'vite';import react from '@vitejs/plugin-react';export default defineConfig({plugins:[react()],resolve:{alias:" + json.dumps(aliases) + "},define:{'process.env.NODE_ENV':JSON.stringify('production')},build:{target:'es2022',cssCodeSplit:false,outDir:" + json.dumps(str(target / "built")) + ",lib:{entry:" + json.dumps(str(source)) + ",name:'PortalInstallFixture',formats:['iife'],fileName:()=> 'host.js',cssFileName:'style'}}});")
    subprocess.run([str(WEB / "node_modules/.bin/vite"), "build", "--config", str(config)], cwd=WEB, check=True)
    state_file = target / "state.json"
    log_file = (target / "server.log").open("w+")
    process = None
    server = None
    try:
        env = dict(os.environ, APP_INSTALL_HTTP_STATE=str(state_file), GOWORK="off", GOTOOLCHAIN="go1.26.10", GOMAXPROCS="2")
        # Never inherit a caller's integration-test database for this fixture.
        env.pop("TEST_POSTGRES_DSN", None)
        process = subprocess.Popen(["go", "test", "-count=1", "-timeout=17m", "./internal/app", "-run", "^TestPlatformManagementBrowserFixture$"], cwd=PORTAL, env=env, stdout=log_file, stderr=subprocess.STDOUT, start_new_session=True)
        deadline = time.monotonic() + 240
        while not state_file.exists():
            if process.poll() is not None:
                log_file.seek(0)
                raise RuntimeError(log_file.read())
            if time.monotonic() > deadline:
                raise TimeoutError("owned installation fixture startup deadline exceeded")
            time.sleep(0.1)
        state = json.loads(state_file.read_text())
        backend = urlsplit(state["url"])

        class Handler(BaseHTTPRequestHandler):
            def relay(self):
                if not self.path.startswith("/api/"):
                    return False
                length = int(self.headers.get("Content-Length", "0"))
                if length > 32768:
                    self.send_error(413)
                    return True
                body = self.rfile.read(length) if length else None
                headers = {key: self.headers[key] for key in ("Cookie", "Origin", "Content-Type", "Host", "Sec-Fetch-Site") if key in self.headers}
                connection = http.client.HTTPConnection(backend.hostname, backend.port, timeout=20)
                try:
                    connection.request(self.command, self.path, body=body, headers=headers)
                    response = connection.getresponse()
                    data = response.read()
                    self.send_response(response.status)
                    for key, value in response.getheaders():
                        if key.lower() not in ("connection", "transfer-encoding", "server", "date", "content-length"):
                            self.send_header(key, value)
                    self.end_headers()
                    self.wfile.write(data)
                finally:
                    connection.close()
                return True

            def do_POST(self):
                if not self.relay():
                    self.send_error(404)

            def do_GET(self):
                if self.relay():
                    return
                path = urlsplit(self.path).path
                if path in ("/", "/manage/apps", "/apps"):
                    data = b'<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Installation development fixture</title><link rel="stylesheet" href="/style.css"><div id="host"></div><script src="/host.js"></script>'
                    mime = "text/html; charset=utf-8"
                elif path in ("/host.js", "/style.css"):
                    data = (target / "built" / path[1:]).read_bytes()
                    mime = "text/javascript" if path.endswith(".js") else "text/css"
                else:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Type", mime)
                self.send_header("Cache-Control", "no-store")
                if mime.startswith("text/html"):
                    self.send_header("Set-Cookie", state["cookieName"] + "=" + state["cookie"] + "; Path=/; HttpOnly; SameSite=Lax")
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        print(f"PROBE_URL=http://127.0.0.1:{server.server_port}/manage/apps", flush=True)
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        if server:
            server.server_close()
        if process:
            Path(str(state_file) + ".stop").touch()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=10)
            if process.returncode != 0:
                log_file.seek(0)
                print(log_file.read())
        log_file.close()
