"""Build the real experimental Portal frame + Apps React root for local browser checks.

Usage: python3 scripts/app-document-browser-fixture.py --apps-dir /path/to/Apps
Loopback only, fixture cookie only, no Portal database, API, credentials or market.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORTAL = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--apps-dir", required=True, type=Path)
args = parser.parse_args()
apps = args.apps_dir.resolve()

with tempfile.TemporaryDirectory(prefix="portal-document-browser-") as directory:
    target = Path(directory)
    (target / "node_modules").symlink_to(apps / "node_modules", target_is_directory=True)
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            status, mime, body = 200, "text/html; charset=utf-8", b""
            if self.path == "/":
                body = b'<!doctype html><meta charset="utf-8"><title>Portal SDK document fixture</title><link rel="stylesheet" href="/host.css"><div id="host"></div><script src="/host.js"></script>'
            elif self.path in ("/host.js", "/host.css"):
                name = "host.js" if self.path.endswith(".js") else "style.css"
                mime = "text/javascript" if name.endswith(".js") else "text/css"
                body = (target / "host" / name).read_bytes()
            elif self.path == "/app-assets/v2-probe.html":
                if "app_document_probe=fixture" not in self.headers.get("Cookie", ""):
                    status, body = 401, b"Unauthorized"
                else:
                    body = (target / "document.html").read_bytes()
            else:
                status, body = 404, b"Not found"
            self.send_response(status)
            self.send_header("Content-Type", mime)
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Referrer-Policy", "no-referrer")
            if self.path == "/":
                self.send_header("Set-Cookie", "app_document_probe=fixture; Path=/; HttpOnly; SameSite=Lax")
                self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'")
            if self.path == "/app-assets/v2-probe.html":
                self.send_header("Content-Security-Policy", (target / "document.html.csp").read_text())
            self.end_headers()
            self.wfile.write(body)
        def log_message(self, *arguments):
            pass
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    origin = f"http://127.0.0.1:{server.server_address[1]}"
    child = target / "child.tsx"
    child.write_text('''import {useState} from 'react';
import {createRoot} from 'react-dom/client';
import {Button} from 'antd';
import {AppRoot,useAppPresentation} from '@portal-app/sdk/root-v2';
function Fixture(){const {presentation}=useAppPresentation();const [count,setCount]=useState(0);return <Button onClick={()=>setCount(count+1)}>{presentation.locale} / {presentation.timeZone} / {count}</Button>}
createRoot(document.getElementById('app')!).render(<AppRoot parentOrigin=''' + json.dumps(origin) + '''><Fixture/></AppRoot>);''')
    host = target / "host.tsx"
    host.write_text('''import {useState} from 'react';
import {createRoot} from 'react-dom/client';
import AppFrameV2 from ''' + json.dumps(str(PORTAL / "web/src/components/apps/AppFrameV2.tsx")) + ''';
const binding={installationId:'00000000-0000-0000-0000-000000000001',generation:'9007199254740993',policyRevision:'1',releaseSha256:'a'.repeat(64)};
const initial={locale:'zh-CN',timeZone:'Asia/Shanghai',layout:'document',theme:{dark:true,colorPrimary:'#1677ff',colorBg:'#141414',colorText:'#ffffff',colorBorder:'#424242',colorBgLayout:'#000000',borderRadius:8}} as const;
function Fixture(){const [presentation,setPresentation]=useState<any>(initial);return <div style={{background:presentation.theme.colorBgLayout,color:presentation.theme.colorText,minHeight:'100vh'}}><h1>Portal SDK document fixture</h1><button onClick={()=>setPresentation({...initial,locale:'zh-TW',timeZone:'Asia/Taipei'})}>Set traditional Chinese</button><button onClick={()=>setPresentation({...initial,locale:'en-US',timeZone:'UTC',theme:{...initial.theme,dark:false,colorBgLayout:'#ffffff',colorBg:'#ffffff',colorText:'#000000'}})}>Set English light</button><AppFrameV2 source="/app-assets/v2-probe.html" title="SDK fixture" binding={binding} presentation={presentation} capabilities={[]} height={360}/></div>}
createRoot(document.getElementById('host')!).render(<Fixture/>);''')
    for name, source, filename in (("child", child, "app.js"), ("host", host, "host.js")):
        out = target / name
        config = target / f"{name}.config.mjs"
        aliases = {package: str(apps / "node_modules" / package) for package in ("react", "react-dom", "antd")}
        config.write_text("import {defineConfig} from 'vite';import react from '@vitejs/plugin-react';export default defineConfig({plugins:[react()],resolve:{alias:" + json.dumps(aliases) + "},define:{'process.env.NODE_ENV':JSON.stringify('production')},build:{target:'es2022',cssCodeSplit:false,outDir:" + json.dumps(str(out)) + ",lib:{entry:" + json.dumps(str(source)) + ",name:'PortalDocumentFixture',formats:['iife'],fileName:()=>" + json.dumps(filename) + ",cssFileName:'style'}}});")
        subprocess.run([str(apps / "node_modules/.bin/vite"), "build", "--config", str(config)], cwd=apps, check=True)
    env = dict(os.environ, APP_FRONTEND_BROWSER_INPUT_DIR=str(target / "child"), APP_FRONTEND_BROWSER_OUTPUT=str(target / "document.html"), GOWORK="off", GOTOOLCHAIN="go1.26.10", GOMAXPROCS="2")
    subprocess.run(["go", "test", "-count=1", "-v", "./internal/appplatform", "-run", "^TestFrontendDocumentBrowserFixture$"], cwd=PORTAL, env=env, check=True)
    print(f"PROBE_URL={origin}/", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
