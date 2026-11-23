import {render,screen,fireEvent} from '@testing-library/react'
import {MemoryRouter,useLocation} from 'react-router'
import {beforeEach,afterEach,expect,test,vi} from 'vitest'
import AppsHub from './AppsHub'
const auth=vi.hoisted(()=>({admin:false}))
vi.mock('../auth',()=>({useAuth:()=>({user:'fixture',can:()=>false,admin:auth.admin})}))
vi.mock('../lib/builtinApps',()=>({BUILTIN_APPS:[],BUILTIN_PIN_PREFIX:'builtin:'}))
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
const prefs=vi.hoisted(()=>({lang:'en-US'}))
vi.mock('../prefs',()=>({usePrefs:()=>prefs}))
const id='00000000-0000-4000-8000-000000000001'
const item={installationId:id,applicationId:'notes-sample',state:'installed',openable:true}
function Location(){return <div data-testid="location">{useLocation().pathname}</div>}
function host(){return <MemoryRouter initialEntries={['/apps']}><AppsHub/><Location/></MemoryRouter>}
beforeEach(()=>{auth.admin=false;vi.stubGlobal('fetch',vi.fn(async input=>new Response(JSON.stringify(String(input)==='/api/apps'?{apps:[]}:{items:[item],nextCursor:''}),{status:200})))})
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals()})
test('authorized v2 app opens the new protected launch page',async()=>{
 render(host());fireEvent.click(await screen.findByRole('button',{name:'notes-sample'}));expect(screen.getByTestId('location').textContent).toBe(`/apps/v2/${id}`)
})
test('a disabled installation is not listed',async()=>{
 vi.stubGlobal('fetch',vi.fn(async input=>new Response(JSON.stringify(String(input)==='/api/apps'?{apps:[]}:{items:[{...item,state:'disabled',openable:false}],nextCursor:''}),{status:200})))
 render(host());await screen.findByText('apps.empty');expect(screen.queryByText('notes-sample')).toBeNull()
})
test('legacy failure does not hide authorized v2 apps',async()=>{
 vi.stubGlobal('fetch',vi.fn(async input=>String(input)==='/api/apps'?new Response('legacy unavailable',{status:500}):new Response(JSON.stringify({items:[item],nextCursor:''}),{status:200})))
 render(host());await screen.findByRole('button',{name:'notes-sample'});expect(screen.queryByText('apps.empty')).toBeNull()
})
test('v2 failure does not turn a known legacy app into an empty directory',async()=>{
 vi.stubGlobal('fetch',vi.fn(async input=>String(input)==='/api/apps'?new Response(JSON.stringify({apps:[{id:'legacy',name:'Legacy app'}]}),{status:200}):new Response('private SQL message',{status:500})))
 render(host());await screen.findByRole('button',{name:'Legacy app'});await screen.findByText('apps.v2ListUnavailable');expect(screen.queryByText(/private SQL/)).toBeNull()
})

test('signed names and descriptions follow panel language without another directory request',async()=>{
 const display={icon:'file-text',texts:[{locale:'en-US',name:'Notes',description:'Personal notes'},{locale:'zh-CN',name:'笔记',description:'个人笔记'},{locale:'zh-TW',name:'筆記',description:'個人筆記'}]}
 vi.stubGlobal('fetch',vi.fn(async input=>new Response(JSON.stringify(String(input)==='/api/apps'?{apps:[]}:{items:[{...item,display}],nextCursor:''}),{status:200})))
 const view=render(host());await screen.findByRole('button',{name:'Notes'});await screen.findByText('Personal notes')
 const calls=vi.mocked(fetch).mock.calls.length
 prefs.lang='zh-TW';view.rerender(host());await screen.findByRole('button',{name:'筆記'});await screen.findByText('個人筆記');expect(fetch).toHaveBeenCalledTimes(calls)
 prefs.lang='en-US'
})

test('platform installations sit in the one saved order beside legacy apps, and an unnamed one follows', async () => {
 const second = '00000000-0000-4000-8000-000000000002'
 const third = '00000000-0000-4000-8000-000000000003'
 vi.stubGlobal('fetch', vi.fn(async input => new Response(JSON.stringify(String(input) === '/api/apps'
  ? { apps: [{ id: 'legacy', name: 'Legacy app' }], order: [`platform:${second}`, 'legacy', `platform:${id}`] }
  : { items: [item, { ...item, installationId: second, applicationId: 'second-app' }, { ...item, installationId: third, applicationId: 'third-app' }], nextCursor: '' }), { status: 200 })))
 render(host())
 await screen.findByRole('button', { name: 'second-app' })
 const names = screen.getAllByRole('button').map(b => b.textContent ?? '')
 const at = (name: string) => names.findIndex(n => n.includes(name))
 expect(at('second-app')).toBeLessThan(at('Legacy app'))
 expect(at('Legacy app')).toBeLessThan(at('notes-sample'))
 expect(at('notes-sample')).toBeLessThan(at('third-app'))
})

const directory=(items:unknown[])=>vi.stubGlobal('fetch',vi.fn(async input=>new Response(JSON.stringify(String(input)==='/api/apps'?{apps:[]}:{items,nextCursor:''}),{status:200})))

test('an app the viewer cannot manage has no Manage item',async()=>{
 render(host());await screen.findByRole('button',{name:'notes-sample'})
 expect(screen.queryByRole('button',{name:/appAdmin.manage/})).toBeNull()
})
test('a manageable app has a Manage item that opens the user-side admin page without opening the app',async()=>{
 directory([{...item,manageable:true}])
 render(host());await screen.findByRole('button',{name:'notes-sample'})
 fireEvent.click(screen.getByRole('button',{name:/appAdmin.manage/}))
 expect(screen.getByTestId('location').textContent).toBe(`/apps/manage/${id}`)
})
test('for a Portal administrator the Manage item opens the console tab',async()=>{
 auth.admin=true;directory([{...item,manageable:true}])
 render(host());await screen.findByRole('button',{name:'notes-sample'})
 fireEvent.click(screen.getByRole('button',{name:/appAdmin.manage/}))
 expect(screen.getByTestId('location').textContent).toBe(`/manage/apps/installations/${id}/admin`)
})
test('an app the viewer can only manage is listed and opens its admin page when clicked, instead of being inert',async()=>{
 directory([{...item,openable:false,manageable:true}])
 render(host());fireEvent.click(await screen.findByRole('button',{name:'notes-sample'}))
 expect(screen.getByTestId('location').textContent).toBe(`/apps/manage/${id}`)
})
test('an app that is neither openable nor manageable stays inert for a reader',async()=>{
 directory([{...item,state:'attention',openable:false,manageable:false}])
 render(host());await screen.findByText('apps.empty')
})
test('a manageable app that needs attention is listed but its card does not open the admin page',async()=>{
 directory([{...item,state:'attention',openable:false,manageable:true}])
 render(host());await screen.findByText('notes-sample')
 expect(screen.queryByRole('button',{name:'notes-sample'})).toBeNull()
})
