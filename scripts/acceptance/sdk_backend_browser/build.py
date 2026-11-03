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
    child.write_text('''import {useEffect,useRef,useState,useLayoutEffect} from 'react';
import {createRoot} from 'react-dom/client';
import {Button,Input,Modal,Space,Typography} from 'antd';
import {AppRoot,useAppBackend,useAppNetwork,useAppNotifications,useAppObjects,useAppReports,useAppPresentation,useAppDraft,useAppNavigation,useAppPage} from '@portal-app/sdk/root-v2';
function MaliciousShell(){useAppPage({title:'Malicious\\nshell',actions:[]});return null}
function RunawayRender(){const [,setTick]=useState(0);useEffect(()=>{setTick(value=>value+1)});return null}
function Fixture(){
 const navigation=useAppNavigation(),[retained,setRetained]=useState(0),[queryEffects,setQueryEffects]=useState(0);
 useLayoutEffect(()=>{setQueryEffects(n=>n+1)},[navigation.query]);
 const backend=useAppBackend(),network=useAppNetwork(),notifications=useAppNotifications(),reports=useAppReports(),objects=useAppObjects(),{presentation}=useAppPresentation();
 const [expanded,setExpanded]=useState(false),[dirty,setDirty]=useState(false);useAppDraft(dirty);
 const [longLabels,setLongLabels]=useState(false);
 const [pageCount,setPageCount]=useState(0),[pageCancelled,setPageCancelled]=useState(0);
 const [fixtureName,setFixtureName]=useState(''),[savedName,setSavedName]=useState('none'),[appDialog,setAppDialog]=useState(false);
 const [malicious,setMalicious]=useState(false),[huge,setHuge]=useState(false),[heightProbe,setHeightProbe]=useState(0),[runaway,setRunaway]=useState(false);
 useAppPage({title:'Fixture '+presentation.locale+' '+navigation.path,description:'Shared page controls',actions:[
  {id:'increment',label:longLabels?'L'.repeat(48):'Header increment',variant:'primary',onAction:()=>{setPageCount(n=>n+1)}},
  {id:'wait',label:'Header wait',onAction:(signal)=>new Promise<void>((_resolve,reject)=>{signal.addEventListener('abort',()=>{setPageCancelled(n=>n+1);reject(new Error('cancelled'))},{once:true})})},
 ]});
 async function oscillateHeight(){for(let i=0;i<12;i++){setHeightProbe(i%2?260:3000);await new Promise(resolve=>setTimeout(resolve,30))}}
 const sequence=useRef(0),[result,setResult]=useState({id:0,text:'none'}),[busy,setBusy]=useState(false);
 async function run(method:string,path:string){const id=++sequence.current;setResult({id,text:'pending'});setBusy(true);try{
  const r=await backend.request({method,path,body:method==='GET'?'':'{"role":"admin","subject":"forged"}'});
  const value=JSON.parse(r.body);setResult({id,text:JSON.stringify({status:r.status,count:value.count,calls:value.calls,role:value.viewer?.role,subject:value.viewer?.sub})});
 }catch(error){setResult({id,text:'error:'+error.code})}finally{setBusy(false)}}
 const networkSequence=useRef(0),[networkResult,setNetworkResult]=useState({id:0,text:'none'}),[networkBusy,setNetworkBusy]=useState(false);
 async function runNetwork(){const id=++networkSequence.current;setNetworkResult({id,text:'pending'});setNetworkBusy(true);try{
  const r=await network.request({method:'GET',url:'https://api.example.com/v1/success',accept:'application/json',contentType:''});
  setNetworkResult({id,text:JSON.stringify({status:r.status,contentType:r.contentType,body:new TextDecoder().decode(r.body)})});
 }catch(error){setNetworkResult({id,text:'error:'+error.code})}finally{setNetworkBusy(false)}}
 const networkStreamSequence=useRef(0),[networkStreamResult,setNetworkStreamResult]=useState({id:0,text:'none'}),[networkStreamBusy,setNetworkStreamBusy]=useState(false);
 async function runNetworkStream(){const id=++networkStreamSequence.current;setNetworkStreamResult({id,text:'pending'});setNetworkStreamBusy(true);try{
  const chunks:number[]=[];let frames=0,started=false;
  const result=await network.stream({method:'GET',url:'https://api.example.com/v1/stream',accept:'application/x-ndjson',contentType:''},{onStart:(status,contentType)=>{started=status===200&&contentType==='application/x-ndjson'},onChunk:async(data,sequence)=>{if(sequence!==frames+1)throw Object.assign(new Error('stream sequence'),{code:'uncertain'});frames++;chunks.push(...data);await new Promise(resolve=>setTimeout(resolve,25))}});
  if(!started)throw Object.assign(new Error('stream start'),{code:'uncertain'});
  setNetworkStreamResult({id,text:JSON.stringify({status:result.status,contentType:result.contentType,bytes:result.bytes,frames:result.frames,body:new TextDecoder().decode(new Uint8Array(chunks))})});
 }catch(error){setNetworkStreamResult({id,text:'error:'+error.code+':started='+started+':frames='+frames+':bytes='+chunks.length})}finally{setNetworkStreamBusy(false)}}
 const credentialSequence=useRef(0),[credentialResult,setCredentialResult]=useState({id:0,text:'none'}),[credentialBusy,setCredentialBusy]=useState(false);
 async function runCredentialNetwork(){const id=++credentialSequence.current;setCredentialResult({id,text:'pending'});setCredentialBusy(true);try{
  const r=await network.request({method:'GET',url:'https://api.example.com/v1/credential',accept:'application/json',contentType:'',credential:'upstream-api'});
  setCredentialResult({id,text:JSON.stringify({status:r.status,contentType:r.contentType,body:new TextDecoder().decode(r.body)})});
 }catch(error){setCredentialResult({id,text:'error:'+error.code})}finally{setCredentialBusy(false)}}
 const notificationSequence=useRef(0),[notificationResult,setNotificationResult]=useState({id:0,text:'none'}),[notificationBusy,setNotificationBusy]=useState(false);
 async function publishNotification(){const id=++notificationSequence.current;setNotificationResult({id,text:'pending'});setNotificationBusy(true);try{
  const result=await notifications.publish({occurrenceId:'browser-notification',title:'Browser notification',message:'Published through the shared SDK.',severity:'info'});
  setNotificationResult({id,text:JSON.stringify(result)});
 }catch(error){setNotificationResult({id,text:'error:'+error.code})}finally{setNotificationBusy(false)}}
 const reportsSequence=useRef(0),[reportsResult,setReportsResult]=useState({id:0,text:'none'}),[reportsBusy,setReportsBusy]=useState(false);
 async function runReports(){const id=++reportsSequence.current;setReportsResult({id,text:'pending'});setReportsBusy(true);try{
  const page=await reports.query({q:'Browser current viewer report',limit:20});
  if(page.count!==1||page.items.length!==1)throw Object.assign(new Error('report count'),{code:'uncertain'});
  const detail=await reports.get(page.items[0].id);
  if(detail.kind!=='get')throw Object.assign(new Error('report detail'),{code:'uncertain'});
  setReportsResult({id,text:JSON.stringify({count:page.count,total:page.total,title:detail.report.title,bodyMarkdown:detail.report.bodyMarkdown})});
 }catch(error){setReportsResult({id,text:'error:'+error.code})}finally{setReportsBusy(false)}}
 const objectsSequence=useRef(0),[objectsResult,setObjectsResult]=useState({id:0,text:'none'}),[objectsBusy,setObjectsBusy]=useState(false);
 async function readObjects(id:number){const page=await objects.list('note',undefined,20);if(page.items.length!==1)throw Object.assign(new Error('note count'),{code:'uncertain'});const note=await objects.get(page.items[0].id);const body=JSON.parse(new TextDecoder().decode(note.content)).body;setObjectsResult({id,text:JSON.stringify({count:page.items.length,label:note.label,body,revision:note.revision})})}
 async function writeObject(){const id=++objectsSequence.current;setObjectsResult({id,text:'pending'});setObjectsBusy(true);try{
  await objects.create({requestId:'browser-note-1',collection:'note',label:'Browser note',mediaType:'application/json',content:new TextEncoder().encode('{"body":"Persisted object"}')});
  await readObjects(id);
 }catch(error){setObjectsResult({id,text:'error:'+error.code})}finally{setObjectsBusy(false)}}
 async function readObject(){const id=++objectsSequence.current;setObjectsResult({id,text:'pending'});setObjectsBusy(true);try{
  await readObjects(id);
 }catch(error){setObjectsResult({id,text:'error:'+error.code})}finally{setObjectsBusy(false)}}
 const editor=navigation.path==='/editor';
 return <div data-testid={editor?'viewport-editor':undefined} role={editor?'region':undefined} aria-label={editor?'Viewport editor':undefined} tabIndex={editor?0:undefined} style={editor?{height:'100%',overflowY:'auto',padding:8}:undefined}>
 <Space orientation="vertical"><Typography.Text data-testid="backend-ready">{String(backend.ready)}</Typography.Text>
 <Typography.Text data-testid="network-ready">{String(network.ready)}</Typography.Text>
 <Typography.Text data-testid="network-stream-ready">{String(network.streamReady)}</Typography.Text>
 <Typography.Text data-testid="credential-ready">{String(network.credentialAvailable)}</Typography.Text>
 <Typography.Text data-testid="notifications-ready">{String(notifications.ready)}</Typography.Text>
 <Typography.Text data-testid="reports-ready">{String(reports.ready)}</Typography.Text>
 <Typography.Text data-testid="objects-ready">{String(objects.ready)}</Typography.Text>
 <Button onClick={()=>setLongLabels(v=>!v)}>Toggle long labels</Button>
 <Typography.Text data-testid="page-count">{pageCount}</Typography.Text>
 <Typography.Text data-testid="page-cancelled">{pageCancelled}</Typography.Text>
 <Typography.Text data-testid="query-effects">{queryEffects}</Typography.Text>
 <Typography.Text data-testid="app-query">{JSON.stringify(navigation.query)}</Typography.Text>
 <Button onClick={()=>navigation.navigate('/',{query:{page:2,q:'中文 & test',active:false,sort:'newest'}})}>Set filters</Button>
 <Typography.Text data-testid="app-route">{navigation.path}</Typography.Text>
 <Button onClick={()=>navigation.navigate('/editor')}>Open editor</Button><Button onClick={()=>navigation.navigate('/')}>Open home</Button>
 <Button onClick={()=>setRetained(n=>n+1)}>Retained {retained}</Button>
 <Typography.Text data-testid="locale">{presentation.locale}</Typography.Text>
 <Typography.Text data-testid="time-zone">{presentation.timeZone}</Typography.Text>
 <Typography.Text data-testid="dark">{String(presentation.theme.dark)}</Typography.Text>
 <label htmlFor="fixture-name">Fixture name</label><Input id="fixture-name" value={fixtureName} maxLength={80} onChange={event=>setFixtureName(event.target.value)} onPressEnter={()=>fixtureName.trim()&&setSavedName(fixtureName)}/><Button disabled={!fixtureName.trim()} onClick={()=>setSavedName(fixtureName)}>Save form</Button><Typography.Text data-testid="saved-name">{savedName}</Typography.Text>
 <Button onClick={()=>setAppDialog(true)}>Open app dialog</Button><Modal open={appDialog} title="App dialog" okText="Confirm app dialog" cancelText="Cancel app dialog" onOk={()=>setAppDialog(false)} onCancel={()=>setAppDialog(false)}><Typography.Text>Application-owned dialog content</Typography.Text></Modal>
 <Button onClick={()=>setExpanded(v=>!v)}>Toggle long content</Button>
 <Button onClick={()=>setDirty(v=>!v)}>{dirty?'Save draft':'Edit draft'}</Button>
 {expanded&&<div data-testid="long-content" style={{height:3000}}>Long document content</div>}
 {editor&&<><Input.TextArea aria-label="Editor body" rows={18}/><div style={{height:2400}}>Long viewport editor content</div><Button>Editor end</Button></>}
 {malicious&&<MaliciousShell/>}{huge&&<div data-testid="huge-height" style={{height:100001}}>Huge content</div>}{heightProbe>0&&<div data-testid="height-probe" style={{height:heightProbe}}>Height probe</div>}{runaway&&<RunawayRender/>}
 <Button disabled={!backend.ready||busy} onClick={()=>run('GET','/v1/echo')}>Read backend</Button>
 <Button disabled={!backend.ready||busy} onClick={()=>run('POST','/v1/echo')}>Write backend</Button>
 <Button disabled={!backend.ready||busy} onClick={()=>run('POST','/v1/echo?disconnect=1')}>Disconnect write</Button>
 <Typography.Text data-testid="backend-result" data-request-id={result.id}>{result.text}</Typography.Text>
 <Button disabled={!network.ready||networkBusy} onClick={()=>runNetwork()}>Read network</Button>
 <Typography.Text data-testid="network-result" data-request-id={networkResult.id}>{networkResult.text}</Typography.Text>
 <Button disabled={!network.streamReady||networkStreamBusy} onClick={()=>runNetworkStream()}>Read network stream</Button>
 <Typography.Text data-testid="network-stream-result" data-request-id={networkStreamResult.id}>{networkStreamResult.text}</Typography.Text>
 <Button disabled={!network.ready||!network.credentialAvailable||credentialBusy} onClick={()=>runCredentialNetwork()}>Read credentialed network</Button>
 <Typography.Text data-testid="credential-result" data-request-id={credentialResult.id}>{credentialResult.text}</Typography.Text>
 <Button disabled={!notifications.ready||notificationBusy} onClick={()=>publishNotification()}>Publish notification</Button>
 <Typography.Text data-testid="notification-result" data-request-id={notificationResult.id}>{notificationResult.text}</Typography.Text>
 <Button disabled={!reports.ready||reportsBusy} onClick={()=>runReports()}>Read reports</Button>
 <Typography.Text data-testid="reports-result" data-request-id={reportsResult.id}>{reportsResult.text}</Typography.Text>
 <Button disabled={!objects.ready||objectsBusy} onClick={()=>writeObject()}>Write object</Button>
 <Button disabled={!objects.ready||objectsBusy} onClick={()=>readObject()}>Read object</Button>
 <Typography.Text data-testid="objects-result" data-request-id={objectsResult.id}>{objectsResult.text}</Typography.Text>
 <Button onClick={()=>setMalicious(true)}>Malicious shell descriptor</Button><Button onClick={()=>setHuge(true)}>Huge document height</Button><Button onClick={()=>void oscillateHeight()}>Oscillate height</Button><Button onClick={()=>setRunaway(true)}>Runaway render</Button></Space></div>
}
createRoot(document.getElementById('app')!).render(<AppRoot parentOrigin=''' + json.dumps(ORIGIN) + '''><Fixture/></AppRoot>);''')
    host = output / "host.tsx"
    host.write_text('''import {useEffect,useMemo,useState} from 'react';import {createRoot} from 'react-dom/client';
import AppFrameV2 from ''' + json.dumps(str(PORTAL / "web/src/components/apps/AppFrameV2.tsx")) + ''';
import AppPageHeader from ''' + json.dumps(str(PORTAL / "web/src/components/apps/AppPageHeader.tsx")) + ''';
import {createBrowserRouter,RouterProvider,Link,useLocation,useNavigate} from 'react-router';
import {AppDraftPrompt,useAppDraftBlocker} from ''' + json.dumps(str(PORTAL / "web/src/components/apps/AppDraftGuard.tsx")) + ''';
const fixtureThemes={dark:{dark:true,colorPrimary:'#1677ff',colorBg:'#141414',colorText:'#ffffff',colorBorder:'#424242',colorBgLayout:'#000000',borderRadius:8},light:{dark:false,colorPrimary:'#1677ff',colorBg:'#ffffff',colorText:'#000000',colorBorder:'#d9d9d9',colorBgLayout:'#f5f5f5',borderRadius:8}};
function initialPresentation(){const dark=localStorage.getItem('sdk-fixture-theme')!=='light';return {locale:'zh-CN',timeZone:'UTC',layout:'document',theme:dark?fixtureThemes.dark:fixtureThemes.light}}
function Fixture(){const location=useLocation(),navigate=useNavigate();const [dirty,setDirty]=useState(false);const blocker=useAppDraftBlocker(dirty);const [launch,setLaunch]=useState<any>(null),[status,setStatus]=useState('loading'),[presentation,setPresentation]=useState<any>(()=>initialPresentation()),[page,setPage]=useState<any>(null);
 const framePresentation=useMemo(()=>launch?{...presentation,layout:launch.routes.find(route=>route.path===location.pathname).layout,navigation:{path:location.pathname,search:location.search,routes:launch.routes}}:null,[launch,presentation,location.pathname,location.search]);
 useEffect(()=>{fetch('/__sdk_fixture/info').then(r=>r.json()).then(info=>fetch(info.launch)).then(async r=>{setStatus('launch:'+r.status);if(r.ok)setLaunch(await r.json())})},[]);
 return <div data-testid="host-surface" style={{background:presentation.theme.colorBgLayout,color:presentation.theme.colorText,minHeight:'100vh'}}><h1>SDK native backend fixture</h1>
 <AppDraftPrompt dirty={dirty} blocker={blocker}/><Link to="/away">Leave fixture</Link><p data-testid="launch-status">{status}</p><button onClick={()=>setPresentation(p=>({...p,locale:'zh-TW'}))}>Traditional Chinese</button>
 <button onClick={()=>setPresentation(p=>({...p,locale:'en-US'}))}>Set English</button>
 <button onClick={()=>setPresentation(p=>({...p,theme:p.theme.dark?fixtureThemes.light:fixtureThemes.dark}))}>Toggle host theme</button>
 <button onClick={()=>setPresentation(p=>({...p,timeZone:'Asia/Taipei'}))}>Set Taipei time</button>
 <button onClick={async()=>{const r=await fetch('/__sdk_fixture/revoke',{method:'POST'});setStatus('revoke:'+r.status)}}>Revoke fixture access</button>
 <AppPageHeader name="SDK native fixture" icon={null} state={page} onBack={()=>{void navigate('/away')}}/>
 {launch&&<AppFrameV2 {...launch} title="SDK native fixture" presentation={framePresentation} onPageChange={setPage} height={500} onDraftChange={setDirty} onNavigate={(path,replace,search)=>{if(blocker.state==='unblocked')void navigate(path+search,{replace})}}/>}</div>}
const router=createBrowserRouter([{path:'/away',element:<p>Fixture closed</p>},{path:'*',element:<Fixture/>}]);
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
