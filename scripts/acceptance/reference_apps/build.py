"""Build both real reference-app packages and the Portal shell for acceptance."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import zipfile

PORTAL = Path(__file__).resolve().parents[3]
ORIGIN = "http://127.0.0.1:31936"


def extract_archive(source, target):
    target.mkdir()
    with zipfile.ZipFile(source) as archive:
        for item in archive.infolist():
            path = Path(item.filename)
            if (
                not item.filename
                or item.is_dir()
                or path.is_absolute()
                or ".." in path.parts
                or len(item.filename) > 512
            ):
                raise ValueError("invalid Notes frontend archive entry")
            destination = target / path
            destination.parent.mkdir(parents=True, exist_ok=True)
            with archive.open(item) as reader, destination.open("wb") as writer:
                while chunk := reader.read(64 * 1024):
                    writer.write(chunk)


def build_package(apps, output, application):
    candidate = output / f"package-{application}"
    subprocess.run(
        [
            "python3",
            str(apps / "scripts/build-app-v2.py"),
            application,
            "--output",
            str(candidate),
        ],
        cwd=apps,
        check=True,
    )
    frontend = candidate / "frontend.zip"
    if not frontend.is_file() or frontend.stat().st_size > 8 * 1024 * 1024:
        raise ValueError(f"{application} frontend package is unavailable or oversized")
    target = output / application
    extract_archive(frontend, target)
    digest = hashlib.sha256(frontend.read_bytes()).hexdigest()
    shutil.rmtree(candidate)
    return digest


def build(apps, output):
    apps, output = apps.resolve(), output.resolve()
    if output.exists() or output.is_symlink():
        raise ValueError("reference-app shell output already exists")
    output.mkdir(parents=True)
    package_digests = {
        application: build_package(apps, output, application)
        for application in ("notes-sample", "scheduled-digest")
    }
    (output / "packages.json").write_text(
        json.dumps(package_digests, indent=2, sort_keys=True) + "\n"
    )
    (output / "node_modules").symlink_to(
        PORTAL / "web/node_modules", target_is_directory=True
    )
    host = output / "host.tsx"
    host.write_text(
        '''import {useEffect} from 'react';
import {createRoot} from 'react-dom/client';
import {ConfigProvider} from 'antd';
import {createBrowserRouter,RouterProvider} from 'react-router';
import {PrefsProvider,usePrefs} from '''
        + json.dumps(str(PORTAL / "web/src/prefs.tsx"))
        + ''';
import {AuthProvider,useAuth} from '''
        + json.dumps(str(PORTAL / "web/src/auth.tsx"))
        + ''';
import {portalTheme} from '''
        + json.dumps(str(PORTAL / "web/src/theme/portal.ts"))
        + ''';
import AppsHub from '''
        + json.dumps(str(PORTAL / "web/src/pages/AppsHub.tsx"))
        + ''';
import AppViewV2 from '''
        + json.dumps(str(PORTAL / "web/src/pages/AppViewV2.tsx"))
        + ''';
const router=createBrowserRouter([{path:'/apps',element:<AppsHub/>},{path:'/apps/v2/:installation/*',element:<AppViewV2/>},{path:'*',element:<AppsHub/>}]);
function Shell(){const prefs=usePrefs();const auth=useAuth();useEffect(()=>{document.documentElement.lang=prefs.lang;document.title='Apps · Portal';document.body.style.margin='0';document.body.style.height='100vh'},[prefs.lang]);return <ConfigProvider locale={prefs.antd} theme={portalTheme(prefs.dark)}><main style={{height:'100vh',boxSizing:'border-box',padding:24,display:'flex',flexDirection:'column',overflow:'hidden',background:prefs.dark?'#000':'#fff',color:prefs.dark?'#fff':'#000'}}><header style={{marginBottom:20,flex:'0 0 auto'}}><strong style={{fontSize:18}}>Portal</strong><nav aria-label="Platform"><span style={{marginLeft:16}}>Apps</span></nav></header>{!auth.loading&&auth.user&&<div style={{flex:'1 1 0',minHeight:0,overflow:'auto',display:'flex',flexDirection:'column'}}><RouterProvider router={router}/></div>}</main></ConfigProvider>}
createRoot(document.getElementById('host')!).render(<PrefsProvider><AuthProvider><Shell/></AuthProvider></PrefsProvider>);'''
    )
    config = output / "host.config.mjs"
    aliases = {
        package: str(PORTAL / "web/node_modules" / package)
        for package in ("react", "react-dom", "antd", "react-router")
    }
    config.write_text(
        "import {defineConfig} from 'vite';import react from '@vitejs/plugin-react';"
        "export default defineConfig({plugins:[react()],resolve:{alias:"
        + json.dumps(aliases)
        + "},define:{'process.env.NODE_ENV':JSON.stringify('production')},"
        "build:{target:'es2022',cssCodeSplit:false,outDir:"
        + json.dumps(str(output / "host"))
        + ",lib:{entry:"
        + json.dumps(str(host))
        + ",name:'ReferenceAppsFixture',formats:['iife'],fileName:()=>'host.js',cssFileName:'style'}}});"
    )
    subprocess.run(
        [str(PORTAL / "web/node_modules/.bin/vite"), "build", "--config", str(config)],
        cwd=PORTAL,
        check=True,
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apps-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    build(args.apps_dir, args.output)
