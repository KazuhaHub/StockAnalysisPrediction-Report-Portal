import {render,screen,waitFor,act,fireEvent} from '@testing-library/react'
import {createMemoryRouter,RouterProvider} from 'react-router'
import {useState,createContext,useContext} from 'react'
import {beforeEach,afterEach,expect,test,vi} from 'vitest'
import {ConfigProvider,theme} from 'antd'
import axe from 'axe-core'
import AppViewV2 from './AppViewV2'
import { CrumbTailProvider, useCrumbTailValue } from '../lib/crumbTail'
function CrumbProbe(){const tail=useCrumbTailValue();return <div data-testid="crumb-tail">{tail===undefined?'pending':tail===null?'none':tail}</div>}
const prefs=vi.hoisted(()=>({lang:'zh-CN',dark:true}))
const frames=vi.hoisted(()=>[] as any[])
const gates=vi.hoisted(()=>({manageable:false,admin:false}))
vi.mock('../lib/appDirectoryV2',async original=>({...await original<typeof import('../lib/appDirectoryV2')>(),useManageable:()=>gates.manageable}))
vi.mock('../auth',async original=>({...await original<typeof import('../auth')>(),useIsAdmin:()=>gates.admin}))
const PrefsPulse=createContext('');
vi.mock('../prefs',()=>({usePrefs:()=>{useContext(PrefsPulse);return prefs}}))
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
vi.mock('../components/apps/AppFrameV2',()=>({default:(props:any)=>{frames.push(props);return <div data-testid="v2-frame">{props.title}</div>}}))
const installation='00000000-0000-4000-8000-000000000001'
const binding={installationId:installation,generation:'9007199254740993',policyRevision:'1',releaseSha256:'a'.repeat(64)}
const response={source:`/app-documents/${installation}/${binding.generation}/1/${'a'.repeat(64)}/${'b'.repeat(64)}/index.html`,binding,capabilities:[],applicationId:'notes-sample',routes:[{id:'home',path:'/',layout:'document'}],layout:'document',timeZone:'Asia/Shanghai'}
const routers:ReturnType<typeof createMemoryRouter>[]=[];
function Page({path=`/apps/v2/${installation}`,algorithm=theme.defaultAlgorithm}:{path?:string;algorithm?:typeof theme.defaultAlgorithm}){const [router]=useState(()=>{const value=createMemoryRouter([{path:'/apps/v2/:installation/*',element:<AppViewV2/>},{path:'/apps',element:<div>Apps home</div>},{path:'/apps/manage/:installation',element:<div>User admin page</div>},{path:'/manage/apps/installations/:installation/admin',element:<div>Console admin tab</div>}],{initialEntries:[path]});routers.push(value);return value});return <ConfigProvider theme={{algorithm}}><PrefsPulse.Provider value={prefs.lang+String(prefs.dark)}><RouterProvider router={router}/></PrefsPulse.Provider></ConfigProvider>}
function page(){return <Page/>}
beforeEach(()=>{routers.splice(0);frames.length=0;gates.manageable=false;gates.admin=false;prefs.lang='zh-CN';prefs.dark=true;vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(response),{status:200})))})
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals()})
test('loads an authorized launch and inherits host appearance without a language selector',async()=>{
 const view=render(page());await screen.findByTestId('v2-frame')
 expect(fetch).toHaveBeenCalledWith(`/api/app-platform/installations/${installation}/launch`,expect.objectContaining({credentials:'same-origin'}))
 expect(frames[frames.length-1].binding.generation).toBe('9007199254740993')
 expect(frames[frames.length-1].presentation).toMatchObject({locale:'zh-CN',timeZone:'Asia/Shanghai',layout:'document',theme:{dark:true}})
 expect(screen.queryByRole('combobox')).toBeNull()
 prefs.lang='zh-TW';prefs.dark=false;view.rerender(page())
 await waitFor(()=>expect(frames[frames.length-1].presentation).toMatchObject({locale:'zh-TW',theme:{dark:false}}))
 expect(fetch).toHaveBeenCalledTimes(1)
})
test.each([{...response,timeZone:''},{...response,binding:{...binding,installationId:'00000000-0000-4000-8000-000000000099'}},{...response,source:'https://evil.invalid/app'},{...response,capabilities:['secrets.read']},{...response,layout:'unknown'}])('invalid launch metadata never mounts executable content',async bad=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(bad),{status:200})))
 render(page());await screen.findByRole('alert');expect(frames).toHaveLength(0)
})
test('refusal is explicit and does not display server diagnostics',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response('private SQL diagnostic',{status:404})))
 render(page());await screen.findByRole('alert');expect(screen.queryByText(/private SQL/)).toBeNull();expect(frames).toHaveLength(0)
})

test('unmount cancels the metadata fetch before late content can mount',async()=>{
 vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(()=>{})))
 const view=render(page())
 await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(1))
 const options=vi.mocked(fetch).mock.calls[0][1]!
 expect(options.signal?.aborted).toBe(false)
 view.unmount();expect(options.signal?.aborted).toBe(true);expect(frames).toHaveLength(0)
})

test('waits for the launch and renders the header once, with the name and never an id first',async()=>{
 let answer!:(value:Response)=>void
 vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{answer=resolve})))
 render(page());await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(1))
 // While the launch is out: the page's one loading state, and nothing of the header, no id, no empty title.
 expect(screen.getByText('apps.v2Loading')).toBeTruthy()
 expect(screen.queryByRole('heading')).toBeNull()
 expect(screen.queryByRole('button',{name:'apps.back'})).toBeNull()
 expect(screen.queryByText(/notes-sample/)).toBeNull()
 const display={icon:'file-text',texts:[{locale:'en-US',name:'Notes',description:'Personal notes'},{locale:'zh-CN',name:'笔记',description:'个人笔记'},{locale:'zh-TW',name:'筆記',description:'個人筆記'}]}
 await act(async()=>answer(new Response(JSON.stringify({...response,display}),{status:200})))
 // The first header on screen is already final.
 expect(await screen.findByRole('heading',{name:'笔记'})).toBeTruthy()
 expect(screen.queryByText(/notes-sample/)).toBeNull()
 expect(screen.queryByText('apps.v2Loading')).toBeNull()
})

test('the shell breadcrumb ends with the app name, from the same launch data and never before it',async()=>{
 const display={icon:'file-text',texts:[{locale:'en-US',name:'Notes',description:'n'},{locale:'zh-CN',name:'笔记',description:'n'},{locale:'zh-TW',name:'筆記',description:'n'}]}
 let answer!:(value:Response)=>void
 vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{answer=resolve})))
 render(<CrumbTailProvider><CrumbProbe/><Page/></CrumbTailProvider>)
 await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(1))
 // While the launch is out the tail is not known: the shell shows no trail, never the shorter one.
 expect(screen.getByTestId('crumb-tail').textContent).toBe('pending')
 await act(async()=>answer(new Response(JSON.stringify({...response,display}),{status:200})))
 await screen.findByRole('heading',{name:'笔记'})
 expect(screen.getByTestId('crumb-tail').textContent).toBe('笔记')
})

test('a failed launch settles the breadcrumb without a name',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response('x',{status:404})))
 render(<CrumbTailProvider><CrumbProbe/><Page/></CrumbTailProvider>)
 await screen.findByRole('alert')
 expect(screen.getByTestId('crumb-tail').textContent).toBe('none')
})

test('signed host title and frame label change language without another launch request',async()=>{
 const display={icon:'file-text',texts:[{locale:'en-US',name:'Notes',description:'Personal notes'},{locale:'zh-CN',name:'笔记',description:'个人笔记'},{locale:'zh-TW',name:'筆記',description:'個人筆記'}]}
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...response,display}),{status:200})))
 const view=render(page());await screen.findByRole('heading',{name:'笔记'})
 expect(frames[frames.length-1].title).toBe('笔记')
 prefs.lang='zh-TW';view.rerender(page());await screen.findByRole('heading',{name:'筆記'})
 expect(frames[frames.length-1].title).toBe('筆記');expect(fetch).toHaveBeenCalledTimes(1)
})


test('signed app routes stay usable while dirty; host history keeps the bounded prompt',async()=>{
 const routes=[{id:'home',path:'/',layout:'document'},{id:'edit',path:'/edit',layout:'viewport'}];
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...response,routes}),{status:200})));
 render(<Page path={`/apps/v2/${installation}/edit`}/>);const element=await screen.findByTestId('v2-frame');
 expect(frames[frames.length-1].presentation).toMatchObject({layout:'viewport',navigation:{path:'/edit',routes}});
 act(()=>frames[frames.length-1].onNavigate('/',false,''));
 await waitFor(()=>expect(frames[frames.length-1].presentation.navigation.path).toBe('/'));
 expect(screen.getByTestId('v2-frame')).toBe(element);expect(fetch).toHaveBeenCalledTimes(1);
 const router=routers[routers.length-1]!;
 await act(async()=>router.navigate(-1));
 await waitFor(()=>expect(frames[frames.length-1].presentation.navigation.path).toBe('/edit'));
 act(()=>frames[frames.length-1].onDraftChange(true));
 act(()=>frames[frames.length-1].onNavigate('/',false,'',false));
 await screen.findByRole('dialog');expect(frames[frames.length-1].presentation.navigation.path).toBe('/edit');
 fireEvent.click(screen.getByRole('button',{name:'apps.draftStay'}));
 await waitFor(()=>expect([...router.state.blockers.values()].every(b=>b.state==='unblocked')).toBe(true));
 expect(router.state.location.pathname).toBe(`/apps/v2/${installation}/edit`);
 act(()=>frames[frames.length-1].onNavigate('/',false,'',true));
 await waitFor(()=>expect(frames[frames.length-1].presentation.navigation.path).toBe('/'));
 await act(async()=>router.navigate('/apps'));await screen.findByRole('dialog');
 fireEvent.click(screen.getByRole('button',{name:'apps.draftLeave'}));
 await waitFor(()=>expect(router.state.location.pathname).toBe('/apps'));
 expect(screen.queryByTestId('v2-frame')).toBeNull();expect(fetch).toHaveBeenCalledTimes(1);
});
test.each(['/unknown','/%65dit','/edit?token=secret','/edit#fragment'])('unsigned or noncanonical deep link never mounts a frame: %s',path=>{
 render(<Page path={`/apps/v2/${installation}${path}`}/>);
 return screen.findByRole('alert').then(()=>expect(frames).toHaveLength(0));
});


test('typed query deep links and history update the same document without refetching',async()=>{
 const query=[{name:'page',type:'integer',min:1,max:100},{name:'q',type:'text',max_bytes:32}];
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...response,routes:[{id:'home',path:'/',layout:'document',query}]}),{status:200})));
 render(<Page path={`/apps/v2/${installation}?page=2`}/>);const frame=await screen.findByTestId('v2-frame');
 expect(frames[frames.length-1].presentation.navigation.search).toBe('?page=2');
 act(()=>frames[frames.length-1].onNavigate('/',false,'?page=3&q=%E4%B8%AD%E6%96%87'));
 await waitFor(()=>expect(frames[frames.length-1].presentation.navigation.search).toBe('?page=3&q=%E4%B8%AD%E6%96%87'));
 const router=routers[routers.length-1];await act(async()=>router.navigate(-1));
 await waitFor(()=>expect(frames[frames.length-1].presentation.navigation.search).toBe('?page=2'));
 expect(screen.getByTestId('v2-frame')).toBe(frame);expect(fetch).toHaveBeenCalledTimes(1);
});


test('invalid query has a host-owned home recovery without refetching or forwarding invalid state',async()=>{
 render(<Page path={`/apps/v2/${installation}?unknown=x`}/>);
 const alert=await screen.findByRole('alert');expect(alert.textContent).toContain('apps.v2InvalidRoute');expect(frames).toHaveLength(0);
 fireEvent.click(screen.getByRole('button',{name:'apps.v2Home'}));await screen.findByTestId('v2-frame');
 expect(frames[frames.length-1].presentation.navigation).toMatchObject({path:'/',search:''});expect(fetch).toHaveBeenCalledTimes(1);
});

test.each([
 ['light',theme.defaultAlgorithm],
 ['dark',theme.darkAlgorithm],
])('has no axe violations for an open app in %s mode',async(_mode,algorithm)=>{
 render(<Page algorithm={algorithm}/>)
 await screen.findByTestId('v2-frame')
 const result=await axe.run(document.body,{rules:{'color-contrast':{enabled:false},region:{enabled:false}}})
 expect(result.violations).toEqual([])
})

test('an unavailable app exposes a keyboard-operable retry and never mounts executable content',async()=>{
 let attempts=0
 vi.stubGlobal('fetch',vi.fn(async()=>{
  attempts++
  return attempts===1
   ? new Response('private failure',{status:503})
   : new Response(JSON.stringify(response),{status:200})
 }))
 render(page())
 const retry=await screen.findByRole('button',{name:'apps.v2Retry'})
 retry.focus()
 fireEvent.keyDown(retry,{key:'Enter',code:'Enter'})
 fireEvent.keyUp(retry,{key:'Enter',code:'Enter'})
 fireEvent.click(retry)
 await screen.findByTestId('v2-frame')
 expect(frames).toHaveLength(1)
 expect(screen.queryByText(/private failure/)).toBeNull()
})
test('a changed network policy relaunches the frame from a fresh launch, and only an administrator may review',async()=>{
 render(page());await screen.findByTestId('v2-frame')
 expect(frames[frames.length-1].canReviewNetwork).toBe(false)
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...response,binding:{...binding,policyRevision:'2'},source:response.source.replace('/1/','/2/')}),{status:200})))
 act(()=>frames[frames.length-1].onNetworkPolicyChanged('2'))
 await waitFor(()=>expect(frames[frames.length-1].binding.policyRevision).toBe('2'))
 expect(fetch).toHaveBeenCalledTimes(1)
})

test('the Manage button appears only for an app the directory marks manageable',async()=>{
 render(page());await screen.findByRole('heading')
 expect(screen.queryByRole('button',{name:'appAdmin.manage'})).toBeNull()
})
test('a delegated administrator goes to the user-side admin page',async()=>{
 gates.manageable=true
 render(page());fireEvent.click(await screen.findByRole('button',{name:'appAdmin.manage'}))
 expect(await screen.findByText('User admin page')).toBeTruthy()
})
test('a Portal administrator goes to the console tab',async()=>{
 gates.manageable=true;gates.admin=true
 render(page());fireEvent.click(await screen.findByRole('button',{name:'appAdmin.manage'}))
 expect(await screen.findByText('Console admin tab')).toBeTruthy()
})
test('the reader page refuses an admin launch and never mounts it',async()=>{
 const admin={...response,surface:'admin',entryPath:'/'}
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(admin),{status:200})))
 render(page());await screen.findByRole('alert');expect(frames).toHaveLength(0)
 expect(fetch).toHaveBeenCalledWith(`/api/app-platform/installations/${installation}/launch`,expect.anything())
})
test('the reader frame is told its surface',async()=>{
 render(page());await screen.findByTestId('v2-frame')
 expect(frames[frames.length-1].surface).toBe('reader')
})
