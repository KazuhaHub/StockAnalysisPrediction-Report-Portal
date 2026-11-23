import {render,screen,waitFor,act,fireEvent} from '@testing-library/react'
import {createMemoryRouter,RouterProvider} from 'react-router'
import {beforeEach,afterEach,expect,test,vi} from 'vitest'
import AppManageV2 from './AppManageV2'
import { CrumbTailProvider, useCrumbTailValue } from '../lib/crumbTail'
function CrumbProbe(){const tail=useCrumbTailValue();return <div data-testid="crumb-tail">{tail===undefined?'pending':tail===null?'none':tail}</div>}
const frames=vi.hoisted(()=>[] as any[])
vi.mock('../prefs',()=>({usePrefs:()=>({lang:'zh-CN',dark:false})}))
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
vi.mock('../components/apps/AppFrameV2',()=>({default:(props:any)=>{frames.push(props);return <div data-testid="v2-frame">{props.title}</div>}}))
const installation='00000000-0000-4000-8000-000000000001'
const binding={installationId:installation,generation:'9007199254740993',policyRevision:'1',releaseSha256:'a'.repeat(64)}
const display={icon:'file-text',texts:[{locale:'en-US',name:'Notes',description:'n'},{locale:'zh-CN',name:'笔记',description:'n'},{locale:'zh-TW',name:'筆記',description:'n'}]}
const response={source:`/app-documents/${installation}/admin/${binding.generation}/1/${'a'.repeat(64)}/${'b'.repeat(64)}/index.html`,binding,capabilities:[],applicationId:'notes-sample',display,
  routes:[{id:'settings',path:'/settings',layout:'document'},{id:'logs',path:'/logs',layout:'document'}],layout:'document',timeZone:'Asia/Shanghai',surface:'admin',entryPath:'/settings',backendAvailable:true}
function page(path=`/apps/manage/${installation}`){
 const router=createMemoryRouter([{path:'/apps/manage/:installation/*',element:<AppManageV2/>},{path:'/apps',element:<div>Apps home</div>}],{initialEntries:[path]})
 return <CrumbTailProvider><CrumbProbe/><RouterProvider router={router}/></CrumbTailProvider>
}
beforeEach(()=>{frames.length=0;vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(response),{status:200})))})
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals()})

test('asks for the admin launch and mounts the frame on the admin surface at the entry route',async()=>{
 render(page());await screen.findByTestId('v2-frame')
 expect(fetch).toHaveBeenCalledTimes(1)
 expect(vi.mocked(fetch).mock.calls[0][0]).toBe(`/api/app-platform/installations/${installation}/admin/launch`)
 const frame=frames[frames.length-1]
 expect(frame).toMatchObject({surface:'admin',backendAvailable:true})
 expect(frame.presentation.navigation).toMatchObject({path:'/settings'})
})
test('refuses a reader launch and never mounts it',async()=>{
 const {surface:_s,entryPath:_e,...reader}=response
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...reader,routes:[{id:'home',path:'/',layout:'document'}]}),{status:200})))
 render(page());await screen.findByRole('alert');expect(frames).toHaveLength(0)
})
test('the first settled render is final: one loading state, then the app name, Back first, and the crumb ends with the name',async()=>{
 let answer!:(value:Response)=>void
 vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{answer=resolve})))
 render(page());await waitFor(()=>expect(fetch).toHaveBeenCalledTimes(1))
 expect(screen.getByText('apps.v2Loading')).toBeTruthy()
 expect(screen.queryByRole('heading')).toBeNull()
 expect(screen.queryByRole('button',{name:/apps.back/})).toBeNull()
 expect(screen.queryByText(/notes-sample/)).toBeNull()
 expect(screen.getByTestId('crumb-tail').textContent).toBe('pending')
 await act(async()=>answer(new Response(JSON.stringify(response),{status:200})))
 const title=await screen.findByRole('heading',{name:'笔记'})
 expect(screen.getByTestId('crumb-tail').textContent).toBe('笔记')
 // Back sits before the title in document order.
 const back=screen.getByRole('button',{name:/apps.back/})
 expect(back.compareDocumentPosition(title)&Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
 expect(screen.queryByText(/notes-sample/)).toBeNull()
})
test('Back leads to the app launcher',async()=>{
 render(page());fireEvent.click(await screen.findByRole('button',{name:/apps.back/}))
 expect(await screen.findByText('Apps home')).toBeTruthy()
})
test('a failed launch settles the breadcrumb without a name and offers a retry',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response('x',{status:403})))
 render(page());await screen.findByRole('alert')
 expect(screen.getByTestId('crumb-tail').textContent).toBe('none')
 expect(screen.getByRole('button',{name:'apps.v2Retry'})).toBeTruthy()
})
test('an app route outside the admin routes never mounts a frame',async()=>{
 render(page(`/apps/manage/${installation}/not-declared`));await screen.findByRole('alert');expect(frames).toHaveLength(0)
})
