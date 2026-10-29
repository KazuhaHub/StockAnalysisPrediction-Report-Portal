import {render,screen,waitFor} from '@testing-library/react'
import {MemoryRouter,Routes,Route} from 'react-router'
import {beforeEach,afterEach,expect,test,vi} from 'vitest'
import AppViewV2 from './AppViewV2'
vi.mock('../components/apps/AppDraftGuard',()=>({default:()=>null}))
const prefs=vi.hoisted(()=>({lang:'zh-CN',dark:true}))
const frames=vi.hoisted(()=>[] as any[])
vi.mock('../prefs',()=>({usePrefs:()=>prefs}))
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
vi.mock('../components/apps/AppFrameV2',()=>({default:(props:any)=>{frames.push(props);return <div data-testid="v2-frame">{props.title}</div>}}))
const installation='00000000-0000-4000-8000-000000000001'
const binding={installationId:installation,generation:'9007199254740993',policyRevision:'1',releaseSha256:'a'.repeat(64)}
const response={source:`/app-documents/${installation}/${binding.generation}/1/${'a'.repeat(64)}/${'b'.repeat(64)}/index.html`,binding,capabilities:[],applicationId:'notes-sample',layout:'document',timeZone:'Asia/Shanghai'}
function page(){return <MemoryRouter initialEntries={[`/apps/v2/${installation}`]}><Routes><Route path="/apps/v2/:installation" element={<AppViewV2/>}/></Routes></MemoryRouter>}
beforeEach(()=>{frames.length=0;prefs.lang='zh-CN';prefs.dark=true;vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(response),{status:200})))})
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

test('signed host title and frame label change language without another launch request',async()=>{
 const display={icon:'file-text',texts:[{locale:'en-US',name:'Notes',description:'Personal notes'},{locale:'zh-CN',name:'笔记',description:'个人笔记'},{locale:'zh-TW',name:'筆記',description:'個人筆記'}]}
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...response,display}),{status:200})))
 const view=render(page());await screen.findByRole('heading',{name:'笔记'})
 expect(frames[frames.length-1].title).toBe('笔记')
 prefs.lang='zh-TW';view.rerender(page());await screen.findByRole('heading',{name:'筆記'})
 expect(frames[frames.length-1].title).toBe('筆記');expect(fetch).toHaveBeenCalledTimes(1)
})
