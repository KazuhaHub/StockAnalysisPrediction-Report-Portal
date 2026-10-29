"""Build real SDK/Portal browser fixtures from an exact verified Apps checkout.

Development acceptance only. Never publishes or reads production configuration.
"""
import argparse
import json
from pathlib import Path
import subprocess
import zipfile

PORTAL = Path(__file__).resolve().parents[3]
ORIGIN = "http://127.0.0.1:31935"


def build(apps, output):
    apps, output = apps.resolve(), output.resolve()
    subprocess.run(["python3", str(PORTAL / "scripts/generate_app_contract.py"), "--check", "--apps-dir", str(apps)], check=True)
    output.mkdir(parents=True, exist_ok=False)
    (output / "node_modules").symlink_to(apps / "node_modules", target_is_directory=True)
    child = output / "child.tsx"
    child.write_text('''import {useRef,useState} from 'react';
import {createRoot} from 'react-dom/client';
import {Button,Space,Typography} from 'antd';
import {AppRoot,useAppBackend,useAppPresentation,useAppDraft} from '@portal-app/sdk/root-v2';
function Fixture(){
 const backend=useAppBackend(),{presentation}=useAppPresentation();
 const [expanded,setExpanded]=useState(false),[dirty,setDirty]=useState(false);useAppDraft(dirty);
 const sequence=useRef(0),[result,setResult]=useState({id:0,text:'none'}),[busy,setBusy]=useState(false);
 async function run(method:string,path:string){const id=++sequence.current;setResult({id,text:'pending'});setBusy(true);try{
  const r=await backend.request({method,path,body:method==='GET'?'':'{"role":"admin","subject":"forged"}'});
  const value=JSON.parse(r.body);setResult({id,text:JSON.stringify({status:r.status,count:value.count,calls:value.calls,role:value.viewer?.role,subject:value.viewer?.sub})});
 }catch(error){setResult({id,text:'error:'+error.code})}finally{setBusy(false)}}
 return <Space orientation="vertical"><Typography.Text data-testid="backend-ready">{String(backend.ready)}</Typography.Text>
 <Typography.Text data-testid="locale">{presentation.locale}</Typography.Text>
 <Button onClick={()=>setExpanded(v=>!v)}>Toggle long content</Button>
 <Button onClick={()=>setDirty(v=>!v)}>{dirty?'Save draft':'Edit draft'}</Button>
 {expanded&&<div data-testid="long-content" style={{height:3000}}>Long document content</div>}
 <Button disabled={!backend.ready||busy} onClick={()=>run('GET','/v1/echo')}>Read backend</Button>
 <Button disabled={!backend.ready||busy} onClick={()=>run('POST','/v1/echo')}>Write backend</Button>
 <Button disabled={!backend.ready||busy} onClick={()=>run('POST','/v1/echo?disconnect=1')}>Disconnect write</Button>
 <Typography.Text data-testid="backend-result" data-request-id={result.id}>{result.text}</Typography.Text></Space>
}
createRoot(document.getElementById('app')!).render(<AppRoot parentOrigin=''' + json.dumps(ORIGIN) + '''><Fixture/></AppRoot>);''')
    host = output / "host.tsx"
    host.write_text('''import {useEffect,useState} from 'react';import {createRoot} from 'react-dom/client';
import AppFrameV2 from ''' + json.dumps(str(PORTAL / "web/src/components/apps/AppFrameV2.tsx")) + ''';
import {createMemoryRouter,RouterProvider,Link} from 'react-router';
import AppDraftGuard from ''' + json.dumps(str(PORTAL / "web/src/components/apps/AppDraftGuard.tsx")) + ''';
const initial={locale:'zh-CN',timeZone:'UTC',layout:'document',theme:{dark:true,colorPrimary:'#1677ff',colorBg:'#141414',colorText:'#ffffff',colorBorder:'#424242',colorBgLayout:'#000000',borderRadius:8}};
function Fixture(){const [dirty,setDirty]=useState(false);const [launch,setLaunch]=useState<any>(null),[status,setStatus]=useState('loading'),[presentation,setPresentation]=useState<any>(initial);
 useEffect(()=>{fetch('/__sdk_fixture/info').then(r=>r.json()).then(info=>fetch(info.launch)).then(async r=>{setStatus('launch:'+r.status);if(r.ok)setLaunch(await r.json())})},[]);
 return <div style={{background:initial.theme.colorBgLayout,color:initial.theme.colorText,minHeight:'100vh'}}><h1>SDK native backend fixture</h1>
 <AppDraftGuard dirty={dirty}/><Link to="/away">Leave fixture</Link><p data-testid="launch-status">{status}</p><button onClick={()=>setPresentation({...initial,locale:'zh-TW'})}>Traditional Chinese</button>
 <button onClick={async()=>{const r=await fetch('/__sdk_fixture/revoke',{method:'POST'});setStatus('revoke:'+r.status)}}>Revoke fixture access</button>
 {launch&&<AppFrameV2 {...launch} title="SDK native fixture" presentation={presentation} height={500} onDraftChange={setDirty}/>}</div>}
const router=createMemoryRouter([{path:'/',element:<Fixture/>},{path:'/away',element:<p>Fixture closed</p>}]);
createRoot(document.getElementById('host')!).render(<RouterProvider router={router}/>);''')
    for name, entry, filename in (("child", child, "app.js"), ("host", host, "host.js")):
        config = output / (name + ".config.mjs")
        aliases = {p: str(apps / "node_modules" / p) for p in ("react", "react-dom", "antd")}
        aliases["react-router"] = str(PORTAL / "web/node_modules/react-router")
        config.write_text("import {defineConfig} from 'vite';import react from '@vitejs/plugin-react';export default defineConfig({plugins:[react()],resolve:{alias:" + json.dumps(aliases) + "},define:{'process.env.NODE_ENV':JSON.stringify('production')},build:{target:'es2022',cssCodeSplit:false,outDir:" + json.dumps(str(output / name)) + ",lib:{entry:" + json.dumps(str(entry)) + ",name:'SdkNativeFixture',formats:['iife'],fileName:()=>" + json.dumps(filename) + ",cssFileName:'style'}}});")
        subprocess.run([str(apps / "node_modules/.bin/vite"), "build", "--config", str(config)], cwd=apps, check=True)
    with zipfile.ZipFile(output / "frontend.zip", "w", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("index.html", '<!doctype html><meta charset="utf-8"><title>Fixture SDK backend</title><link rel="stylesheet" href="style.css"><div id="app"></div><script src="app.js"></script>')
        for filename in ("app.js", "style.css"):
            archive.write(output / "child" / filename, filename)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apps-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    build(args.apps_dir, args.output)
