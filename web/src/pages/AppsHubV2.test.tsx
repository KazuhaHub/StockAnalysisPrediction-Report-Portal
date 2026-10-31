import {render,screen,fireEvent} from '@testing-library/react'
import {MemoryRouter,useLocation} from 'react-router'
import {beforeEach,afterEach,expect,test,vi} from 'vitest'
import AppsHub from './AppsHub'
vi.mock('../auth',()=>({useAuth:()=>({user:'fixture',can:()=>false})}))
vi.mock('../lib/builtinApps',()=>({BUILTIN_APPS:[],BUILTIN_PIN_PREFIX:'builtin:'}))
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
const prefs=vi.hoisted(()=>({lang:'en-US'}))
vi.mock('../prefs',()=>({usePrefs:()=>prefs}))
const id='00000000-0000-4000-8000-000000000001'
const item={installationId:id,applicationId:'notes-sample',state:'installed',openable:true}
function Location(){return <div data-testid="location">{useLocation().pathname}</div>}
function host(){return <MemoryRouter initialEntries={['/apps']}><AppsHub/><Location/></MemoryRouter>}
beforeEach(()=>{vi.stubGlobal('fetch',vi.fn(async input=>new Response(JSON.stringify(String(input)==='/api/apps'?{apps:[]}:{items:[item],nextCursor:''}),{status:200})))})
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals()})
test('authorized v2 app opens the new protected launch page',async()=>{
 render(host());fireEvent.click(await screen.findByRole('button',{name:'notes-sample'}));expect(screen.getByTestId('location').textContent).toBe(`/apps/v2/${id}`)
})
test('disabled installation is visible with no open action',async()=>{
 vi.stubGlobal('fetch',vi.fn(async input=>new Response(JSON.stringify(String(input)==='/api/apps'?{apps:[]}:{items:[{...item,state:'disabled',openable:false}],nextCursor:''}),{status:200})))
 render(host());const card=await screen.findByRole('button',{name:'notes-sample'});expect(card.getAttribute('aria-disabled')).toBe('true');fireEvent.click(card);expect(screen.getByTestId('location').textContent).toBe('/apps')
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
