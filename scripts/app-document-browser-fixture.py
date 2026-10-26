"""Build the real experimental Portal frame + Apps React root for local browser checks.

Usage: python3 scripts/app-document-browser-fixture.py --apps-dir /path/to/Apps
Loopback only; synthetic fixture cookies and disposable data; no production access.
"""
import argparse
import http.client
import time
from urllib.parse import urlsplit
import json
import os
import signal
from pathlib import Path
import subprocess
import tempfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORTAL = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--apps-dir", required=True, type=Path)
parser.add_argument("--portal-http", action="store_true", help="Use actual Portal HTTP/session/DB handlers in a disposable fixture")
parser.add_argument("--launch-page", action="store_true", help="Exercise the actual AppViewV2 page with real Portal preferences")
args = parser.parse_args()
if args.launch_page and not args.portal_http:
    parser.error("--launch-page requires --portal-http")
apps = args.apps_dir.resolve()

with tempfile.TemporaryDirectory(prefix="portal-document-browser-") as directory:
    target = Path(directory)
    (target / "node_modules").symlink_to(apps / "node_modules", target_is_directory=True)
    portal_state = None
    class Handler(BaseHTTPRequestHandler):
        def relay(self):
            if not args.portal_http or not (self.path.startswith("/api/app-platform/") or self.path.startswith("/app-documents/") or self.path.startswith("/__fixture/")):
                return False
            address = urlsplit(portal_state["url"])
            connection = http.client.HTTPConnection(address.hostname, address.port, timeout=10)
            length = int(self.headers.get("Content-Length", "0"))
            if length > 1024:
                self.send_error(413)
                return True
            body = self.rfile.read(length) if length else None
            headers = {key: self.headers[key] for key in ("Cookie", "Origin", "Content-Type", "Accept", "If-None-Match", "Range") if key in self.headers}
            try:
                connection.request(self.command, self.path, body=body, headers=headers)
                response = connection.getresponse()
                content = response.read()
                self.send_response(response.status)
                for key, value in response.getheaders():
                    if key.lower() not in ("connection", "transfer-encoding", "server", "date"):
                        self.send_header(key, value)
                self.end_headers()
                self.wfile.write(content)
            finally:
                connection.close()
            return True
        def do_POST(self):
            if not self.relay():
                self.send_error(404)
        def do_GET(self):
            if self.relay():
                return
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
                cookie = (portal_state["cookieName"] + "=" + portal_state["cookie"]) if args.portal_http else "app_document_probe=fixture"
                self.send_header("Set-Cookie", cookie + "; Path=/; HttpOnly; SameSite=Lax")
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
    if args.portal_http:
        host.write_text('''import {useEffect,useState} from 'react';
import {createRoot} from 'react-dom/client';
import AppFrameV2 from ''' + json.dumps(str(PORTAL / "web/src/components/apps/AppFrameV2.tsx")) + ''';
const initial={locale:'zh-CN',timeZone:'Asia/Shanghai',layout:'document',theme:{dark:true,colorPrimary:'#1677ff',colorBg:'#141414',colorText:'#ffffff',colorBorder:'#424242',colorBgLayout:'#000000',borderRadius:8}};
function Fixture(){const [presentation,setPresentation]=useState<any>(initial);const [launch,setLaunch]=useState<any>(null);const [status,setStatus]=useState('Loading launch');const [lastSource,setLastSource]=useState('');
async function open(){setLaunch(null);const info=await fetch('/__fixture/info').then(r=>r.json());const response=await fetch(info.launch);setStatus('Launch HTTP '+response.status);if(response.ok){const next=await response.json();setLastSource(next.source);setLaunch(next)}}
async function action(name:string){const r=await fetch('/__fixture/'+name,{method:'POST'});setStatus(name+' HTTP '+r.status)}
async function check(){const r=await fetch(lastSource);setStatus('Document HTTP '+r.status)}
useEffect(()=>{open()},[]);
return <div style={{background:presentation.theme.colorBgLayout,color:presentation.theme.colorText,minHeight:'100vh'}}><h1>Portal HTTP and SDK fixture</h1><p role="status">{status}</p><button onClick={open}>Reopen app</button><button onClick={()=>setPresentation({...initial,locale:'zh-TW',timeZone:'Asia/Taipei'})}>Set traditional Chinese</button><button onClick={()=>setPresentation({...initial,locale:'en-US',timeZone:'UTC',theme:{...initial.theme,dark:false,colorBgLayout:'#ffffff',colorBg:'#ffffff',colorText:'#000000'}})}>Set English light</button><button onClick={()=>action('revoke-access')}>Revoke fixture access</button><button onClick={()=>action('restore-access')}>Restore fixture access</button><button onClick={()=>action('revoke-session')}>Revoke fixture session</button><button onClick={check} disabled={!lastSource}>Check last document</button>{launch&&<AppFrameV2 {...launch} title="SDK fixture" presentation={presentation} height={360}/>}</div>}
createRoot(document.getElementById('host')!).render(<Fixture/>);''')
    if args.launch_page:
        host.write_text('''import {useEffect,useState} from 'react';
import {createRoot} from 'react-dom/client';
import {ConfigProvider,App as AntdApp,theme} from 'antd';
import {MemoryRouter,Routes,Route} from 'react-router';
import {PrefsProvider,usePrefs} from ''' + json.dumps(str(PORTAL / "web/src/prefs.tsx")) + ''';
import AppViewV2 from ''' + json.dumps(str(PORTAL / "web/src/pages/AppViewV2.tsx")) + ''';
function Host(){const prefs=usePrefs();const [path,setPath]=useState('');useEffect(()=>{fetch('/__fixture/info').then(r=>r.json()).then(info=>setPath('/apps/v2/'+info.launch.split('/')[4]))},[]);return <ConfigProvider locale={prefs.antd} theme={{algorithm:prefs.dark?theme.darkAlgorithm:theme.defaultAlgorithm}}><AntdApp><div style={{display:'flex',flexDirection:'column',height:'100dvh',background:prefs.dark?'#000':'#fff'}}><div><button onClick={()=>prefs.setLang('zh-CN')}>Simplified Chinese</button><button onClick={()=>prefs.setLang('zh-TW')}>Traditional Chinese</button><button onClick={()=>prefs.setMode(prefs.dark?'light':'dark')}>Toggle host theme</button></div>{path&&<MemoryRouter initialEntries={[path]}><Routes><Route path="/apps/v2/:installation" element={<AppViewV2/>}/><Route path="/apps" element={<div>Apps home</div>}/></Routes></MemoryRouter>}</div></AntdApp></ConfigProvider>}
createRoot(document.getElementById('host')!).render(<PrefsProvider><Host/></PrefsProvider>);''')
    for name, source, filename in (("child", child, "app.js"), ("host", host, "host.js")):
        out = target / name
        config = target / f"{name}.config.mjs"
        aliases = {package: str(apps / "node_modules" / package) for package in ("react", "react-dom", "antd")}
        if args.launch_page:
            aliases["react-router"] = str(PORTAL / "web/node_modules/react-router")
        config.write_text("import {defineConfig} from 'vite';import react from '@vitejs/plugin-react';export default defineConfig({plugins:[react()],resolve:{alias:" + json.dumps(aliases) + "},define:{'process.env.NODE_ENV':JSON.stringify('production')},build:{target:'es2022',cssCodeSplit:false,outDir:" + json.dumps(str(out)) + ",lib:{entry:" + json.dumps(str(source)) + ",name:'PortalDocumentFixture',formats:['iife'],fileName:()=>" + json.dumps(filename) + ",cssFileName:'style'}}});")
        subprocess.run([str(apps / "node_modules/.bin/vite"), "build", "--config", str(config)], cwd=apps, check=True)
    process = None
    log_file = None
    state_file = target / "portal-state.json"
    try:
        if args.portal_http:
            env = dict(os.environ, APP_DOCUMENT_HTTP_FIXTURE_DIR=str(target), APP_DOCUMENT_HTTP_STATE=str(state_file), APP_DOCUMENT_HTTP_ORIGIN=origin, APP_DOCUMENT_HTTP_NAMED_TIMEZONE="UTC" if args.launch_page else "", GOWORK="off", GOTOOLCHAIN="go1.26.10", GOMAXPROCS="2")
            log_file = (target / "portal-http.log").open("w+")
            process = subprocess.Popen(["go", "test", "-count=1", "-timeout=25m", "./internal/app", "-run", "^TestPlatformHTTPBrowserFixture$"], cwd=PORTAL, env=env, stdout=log_file, stderr=subprocess.STDOUT, start_new_session=True)
            deadline = time.monotonic() + 240
            while True:
                if process.poll() is not None or time.monotonic() > deadline:
                    log_file.seek(0)
                    raise RuntimeError("Portal fixture did not start: " + log_file.read())
                try:
                    portal_state = json.loads(state_file.read_text())
                    break
                except (FileNotFoundError, json.JSONDecodeError):
                    time.sleep(0.1)
        else:
            env = dict(os.environ, APP_FRONTEND_BROWSER_INPUT_DIR=str(target / "child"), APP_FRONTEND_BROWSER_OUTPUT=str(target / "document.html"), GOWORK="off", GOTOOLCHAIN="go1.26.10", GOMAXPROCS="2")
            subprocess.run(["go", "test", "-count=1", "-v", "./internal/appplatform", "-run", "^TestFrontendDocumentBrowserFixture$"], cwd=PORTAL, env=env, check=True)
        print(f"PROBE_URL={origin}/", flush=True)
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        if process is not None:
            Path(str(state_file) + ".stop").touch()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                process.wait(timeout=10)
            if process.returncode != 0:
                log_file.seek(0)
                print(log_file.read())
        if log_file is not None:
            log_file.close()
