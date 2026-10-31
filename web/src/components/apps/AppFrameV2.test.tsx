import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import type { BridgeHostOptions } from '../../lib/bridge-v2'
import AppFrameV2 from './AppFrameV2'

const sessions=vi.hoisted(()=>[] as {options:BridgeHostOptions;close:ReturnType<typeof vi.fn>;updatePresentation:ReturnType<typeof vi.fn>}[])
vi.mock('../../lib/bridge-v2',async original=>({...await original<typeof import('../../lib/bridge-v2')>(),createBridgeHost:(options:BridgeHostOptions)=>{const session={options,close:vi.fn(),updatePresentation:vi.fn(()=>true)};sessions.push(session);return session}}))
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
afterEach(()=>{sessions.splice(0);vi.restoreAllMocks()})
const binding={installationId:'00000000-0000-0000-0000-000000000001',generation:'9007199254740993',policyRevision:'1',releaseSha256:'a'.repeat(64)}
const presentation={locale:'zh-CN',timeZone:'Asia/Shanghai',layout:'document',theme:{dark:true,colorPrimary:'#1677ff',colorBg:'#141414',colorText:'#ffffff',colorBorder:'#424242',colorBgLayout:'#000000',borderRadius:8}} as const
const props={source:'/app-assets/fixture/index.html',binding,presentation,capabilities:[],title:'Fixture',height:640}

test('frame is covered before load and remains hidden until the current session is ready',()=>{
 render(<AppFrameV2 {...props}/>);const frame=screen.getByTitle('Fixture')
 expect(frame.getAttribute('sandbox')).toBe('allow-scripts');expect(frame.getAttribute('referrerpolicy')).toBe('no-referrer');expect(frame.getAttribute('aria-hidden')).toBe('true');expect(frame.style.visibility).toBe('hidden');expect(frame.style.backgroundColor).toBe('rgb(0, 0, 0)');expect(frame.style.colorScheme).toBe('dark');expect(frame.style.border).toBe('0px');expect(screen.getByRole('status').textContent).toBe('apps.v2Loading')
 fireEvent.load(frame);expect(frame.style.visibility).toBe('hidden');expect(sessions).toHaveLength(1)
 act(()=>{sessions[0].options.onReady?.(presentation);sessions[0].options.onDocumentHeight?.(640)});expect(frame.style.visibility).toBe('visible');expect(frame.hasAttribute('aria-hidden')).toBe(false);expect(screen.queryByRole('status')).toBeNull()
})
test('reload fences the old ready callback and restores a themed cover',()=>{
 render(<AppFrameV2 {...props}/>);const frame=screen.getByTitle('Fixture');fireEvent.load(frame);const old=sessions[0];act(()=>{old.options.onReady?.(presentation);old.options.onDocumentHeight?.(640)});fireEvent.load(frame)
 expect(old.close).toHaveBeenCalledTimes(1);expect(frame.style.visibility).toBe('hidden');act(()=>{old.options.onReady?.(presentation);old.options.onDocumentHeight?.(640)});act(()=>old.options.onFailure?.('transport_error'));expect(screen.queryByRole('alert')).toBeNull();expect(frame.style.visibility).toBe('hidden');act(()=>{sessions[1].options.onReady?.(presentation);sessions[1].options.onDocumentHeight?.(640)});expect(frame.style.visibility).toBe('visible')
})
test('presentation updates do not navigate the frame; binding changes replace it',()=>{
 const view=render(<AppFrameV2 {...props}/>);const frame=screen.getByTitle('Fixture');fireEvent.load(frame);act(()=>{sessions[0].options.onReady?.(presentation);sessions[0].options.onDocumentHeight?.(640)})
 const changed={...presentation,locale:'zh-TW' as const,theme:{...presentation.theme,dark:false,colorBgLayout:'#ffffff'}}
 view.rerender(<AppFrameV2 {...props} presentation={changed}/>);expect(screen.getByTitle('Fixture')).toBe(frame);expect(sessions[0].updatePresentation).toHaveBeenLastCalledWith(changed);expect(frame.style.backgroundColor).toBe('rgb(255, 255, 255)');expect(frame.style.colorScheme).toBe('light')
 view.rerender(<AppFrameV2 {...props} binding={{...binding,generation:'9007199254740994'}}/>);expect(screen.getByTitle('Fixture')).not.toBe(frame);expect(sessions[0].close).toHaveBeenCalledTimes(1);expect(screen.getByTitle('Fixture').style.visibility).toBe('hidden')
})
test('failure removes executable frame and retry creates a new covered document',()=>{
 const view=render(<AppFrameV2 {...props}/>);let frame=screen.getByTitle('Fixture');fireEvent.load(frame);const old=sessions[0];act(()=>old.options.onFailure?.('protocol_error'))
 expect(screen.queryByTitle('Fixture')).toBeNull();expect(screen.getByRole('alert').textContent).toContain('apps.v2Failed');expect(old.close).toHaveBeenCalledTimes(1)
 fireEvent.click(screen.getByRole('button',{name:'apps.v2Retry'}));frame=screen.getByTitle('Fixture');expect(frame.style.visibility).toBe('hidden');act(()=>{old.options.onReady?.(presentation);old.options.onDocumentHeight?.(640)});expect(frame.style.visibility).toBe('hidden');fireEvent.load(frame);view.unmount();expect(sessions[1].close).toHaveBeenCalledTimes(1)
})
test.each(['javascript:alert(1)','https://evil.invalid/app','//evil.invalid/app','/app-assets/fixture/index.html?token=secret','/app-assets/fixture/index.html#fragment'])('unsafe executable source is refused before iframe creation: %s',source=>{
 render(<AppFrameV2 {...props} source={source}/>);expect(screen.queryByTitle('Fixture')).toBeNull();expect(screen.getByRole('alert')).toBeTruthy();expect(sessions).toHaveLength(0)
})

test('asset delivery has a bounded deadline even if iframe load never fires',()=>{
 vi.useFakeTimers()
 try{
  const view=render(<AppFrameV2 {...props}/>);act(()=>vi.advanceTimersByTime(10000));expect(screen.queryByTitle('Fixture')).toBeNull();expect(screen.getByRole('alert')).toBeTruthy();view.unmount()
 }finally{vi.useRealTimers()}
})

test('private document URL must match the bridge activation binding',()=>{
 const source=`/app-documents/${binding.installationId}/${binding.generation}/${binding.policyRevision}/${binding.releaseSha256}/${'b'.repeat(64)}/index.html`
 const view=render(<AppFrameV2 {...props} source={source}/>)
 expect(screen.getByTitle('Fixture').getAttribute('src')).toBe(source)
 view.rerender(<AppFrameV2 {...props} source={source} binding={{...binding,generation:'2'}}/>)
 expect(screen.queryByTitle('Fixture')).toBeNull()
})

test('only an advertised own-backend channel receives the parent transport and changes retire the frame',()=>{
 const view=render(<AppFrameV2 {...props}/>);fireEvent.load(screen.getByTitle('Fixture'))
 expect(sessions[0].options.onBackendRequest).toBeUndefined()
 const old=screen.getByTitle('Fixture')
 view.rerender(<AppFrameV2 {...props} backendAvailable/>);expect(screen.getByTitle('Fixture')).not.toBe(old)
 expect(sessions[0].close).toHaveBeenCalledOnce();fireEvent.load(screen.getByTitle('Fixture'))
 expect(sessions[1].options.onBackendRequest).toBeTypeOf('function')
})

test('network transport exists only for a granted network capability and capability changes retire the frame',()=>{
 const view=render(<AppFrameV2 {...props}/>);const old=screen.getByTitle('Fixture');fireEvent.load(old)
 expect(sessions[0].options.onNetworkRequest).toBeUndefined()
 view.rerender(<AppFrameV2 {...props} capabilities={['network.http']}/>)
 expect(screen.getByTitle('Fixture')).not.toBe(old);expect(sessions[0].close).toHaveBeenCalledOnce()
 fireEvent.load(screen.getByTitle('Fixture'))
 expect(sessions[1].options.onNetworkRequest).toBeTypeOf('function')
})

test('report transport exists only for a current reports.read grant and revocation retires the frame',()=>{
 const view=render(<AppFrameV2 {...props}/>);const old=screen.getByTitle('Fixture');fireEvent.load(old)
 expect(sessions[0].options.onReportRequest).toBeUndefined()
 view.rerender(<AppFrameV2 {...props} capabilities={['reports.read']}/>)
 expect(screen.getByTitle('Fixture')).not.toBe(old);expect(sessions[0].close).toHaveBeenCalledOnce()
 fireEvent.load(screen.getByTitle('Fixture'))
 expect(sessions[1].options.onReportRequest).toBeTypeOf('function')
 const current=screen.getByTitle('Fixture')
 view.rerender(<AppFrameV2 {...props} capabilities={[]}/>)
 expect(screen.getByTitle('Fixture')).not.toBe(current);expect(sessions[1].close).toHaveBeenCalledOnce()
 fireEvent.load(screen.getByTitle('Fixture'))
 expect(sessions[2].options.onReportRequest).toBeUndefined()
})

test('object transport exists only for a current storage.files grant and revocation retires the frame',()=>{
 const view=render(<AppFrameV2 {...props}/>);const old=screen.getByTitle('Fixture');fireEvent.load(old)
 expect(sessions[0].options.onObjectRequest).toBeUndefined()
 view.rerender(<AppFrameV2 {...props} capabilities={['storage.files']}/>)
 expect(screen.getByTitle('Fixture')).not.toBe(old);expect(sessions[0].close).toHaveBeenCalledOnce()
 fireEvent.load(screen.getByTitle('Fixture'))
 expect(sessions[1].options.onObjectRequest).toBeTypeOf('function')
 const current=screen.getByTitle('Fixture')
 view.rerender(<AppFrameV2 {...props} capabilities={[]}/>)
 expect(screen.getByTitle('Fixture')).not.toBe(current);expect(sessions[1].close).toHaveBeenCalledOnce()
 fireEvent.load(screen.getByTitle('Fixture'))
 expect(sessions[2].options.onObjectRequest).toBeUndefined()
})

test('document reveal waits for height and content can grow and shrink in one iframe',()=>{
 render(<AppFrameV2 {...props}/>);const frame=screen.getByTitle('Fixture');fireEvent.load(frame)
 act(()=>sessions[0].options.onReady?.(presentation));expect(frame.style.visibility).toBe('hidden')
 act(()=>sessions[0].options.onDocumentHeight?.(24000));expect(frame.style.visibility).toBe('visible');expect(frame.parentElement!.style.height).toBe('24000px')
 act(()=>sessions[0].options.onDocumentHeight?.(120));expect(screen.getByTitle('Fixture')).toBe(frame);expect(frame.parentElement!.style.height).toBe('120px')
 fireEvent.load(frame);act(()=>sessions[1].options.onReady?.(presentation));expect(frame.style.visibility).toBe('hidden')
 act(()=>sessions[0].options.onDocumentHeight?.(500));expect(frame.style.visibility).toBe('hidden')
 act(()=>sessions[1].options.onDocumentHeight?.(0));expect(frame.parentElement!.style.height).toBe('64px')
})

test('missing initial document measurement fails with a bounded retry surface',()=>{
 vi.useFakeTimers();try{
  render(<AppFrameV2 {...props}/>);fireEvent.load(screen.getByTitle('Fixture'));act(()=>sessions[0].options.onReady?.(presentation));
  act(()=>vi.advanceTimersByTime(10000));expect(screen.queryByTitle('Fixture')).toBeNull();expect(screen.getByRole('alert')).toBeTruthy()
 }finally{vi.useRealTimers()}
})

test('draft state retires on reload, failure and unmount; stale channels cannot restore it',()=>{
 const changed=vi.fn();const view=render(<AppFrameV2 {...props} onDraftChange={changed}/>);
 const frame=screen.getByTitle('Fixture');fireEvent.load(frame);const first=sessions[0];
 act(()=>first.options.onDraftChange?.(true));expect(changed).toHaveBeenLastCalledWith(true);
 fireEvent.load(frame);expect(changed).toHaveBeenLastCalledWith(false);
 act(()=>first.options.onDraftChange?.(true));expect(changed).toHaveBeenLastCalledWith(false);
 act(()=>sessions[1].options.onDraftChange?.(true));expect(changed).toHaveBeenLastCalledWith(true);
 act(()=>sessions[1].options.onFailure?.('protocol_error'));expect(changed).toHaveBeenLastCalledWith(false);
 fireEvent.click(screen.getByRole('button',{name:'apps.v2Retry'}));fireEvent.load(screen.getByTitle('Fixture'));
 const last=sessions[2];act(()=>last.options.onDraftChange?.(true));view.unmount();
 expect(changed).toHaveBeenLastCalledWith(false);changed.mockClear();act(()=>last.options.onDraftChange?.(true));expect(changed).not.toHaveBeenCalled();
})


test('navigation callbacks from a replaced channel cannot move the host',()=>{
 const navigate=vi.fn();const view=render(<AppFrameV2 {...props} onNavigate={navigate}/>);const frame=screen.getByTitle('Fixture');fireEvent.load(frame);const first=sessions[0];
 act(()=>first.options.onNavigate?.('/editor',true,''));expect(navigate).toHaveBeenCalledExactlyOnceWith('/editor',true,'');
 fireEvent.load(frame);navigate.mockClear();act(()=>first.options.onNavigate?.('/',false,''));expect(navigate).not.toHaveBeenCalled();
 act(()=>sessions[1].options.onNavigate?.('/',false,''));expect(navigate).toHaveBeenCalledExactlyOnceWith('/',false,'');
 view.unmount();navigate.mockClear();act(()=>sessions[1].options.onNavigate?.('/editor',false,''));expect(navigate).not.toHaveBeenCalled();
})

test('page metadata cannot return through a retired document observer',()=>{
 const changed=vi.fn();const view=render(<AppFrameV2 {...props} onPageChange={changed}/>);
 const frame=screen.getByTitle('Fixture');fireEvent.load(frame);const first=sessions[0];
 const state={page:{title:'Page',description:'',actions:[]},ready:true,busyAction:null,outcome:null,invoke:()=>false} as const;
 act(()=>first.options.onPageChange?.(state));expect(changed).toHaveBeenLastCalledWith(state);
 fireEvent.load(frame);expect(changed).toHaveBeenLastCalledWith(null);
 act(()=>first.options.onPageChange?.(state));expect(changed).toHaveBeenLastCalledWith(null);
 act(()=>sessions[1].options.onPageChange?.(state));expect(changed).toHaveBeenLastCalledWith(state);
 view.unmount();expect(changed).toHaveBeenLastCalledWith(null);
 act(()=>sessions[1].options.onPageChange?.(state));expect(changed).toHaveBeenLastCalledWith(null);
});
