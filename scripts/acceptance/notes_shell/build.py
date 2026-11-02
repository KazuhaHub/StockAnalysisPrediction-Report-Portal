"""Build the real Notes package and Portal application shell for browser acceptance."""
import argparse
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


def build(apps, output):
    apps, output = apps.resolve(), output.resolve()
    if output.exists() or output.is_symlink():
        raise ValueError("Notes shell output already exists")
    output.mkdir(parents=True)
    candidate = output / "package"
    subprocess.run(
        [
            "python3",
            str(apps / "scripts/build-app-v2.py"),
            "notes-sample",
            "--output",
            str(candidate),
        ],
        cwd=apps,
        check=True,
    )
    frontend = candidate / "frontend.zip"
    if not frontend.is_file() or frontend.stat().st_size > 8 * 1024 * 1024:
        raise ValueError("Notes frontend package is unavailable or oversized")

    extract_archive(frontend, output / "notes")
    shutil.rmtree(candidate)
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
import {portalTheme} from '''
        + json.dumps(str(PORTAL / "web/src/theme/portal.ts"))
        + ''';
import AppViewV2 from '''
        + json.dumps(str(PORTAL / "web/src/pages/AppViewV2.tsx"))
        + ''';
function Shell(){const prefs=usePrefs();useEffect(()=>{document.documentElement.lang=prefs.lang;document.title='Notes · Portal';document.body.style.margin='0';document.body.style.height='100vh'},[prefs.lang]);return <ConfigProvider locale={prefs.antd} theme={portalTheme(prefs.dark)}><div style={{height:'100vh',display:'flex',flexDirection:'column',overflow:'hidden'}}><RouterProvider router={createBrowserRouter([{path:'/apps/v2/:installation/*',element:<AppViewV2/>},{path:'*',element:<AppViewV2/>}])}/></div></ConfigProvider>}
createRoot(document.getElementById('host')!).render(<PrefsProvider><Shell/></PrefsProvider>);'''
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
        + ",name:'NotesShellFixture',formats:['iife'],fileName:()=>'host.js',cssFileName:'style'}}});"
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
