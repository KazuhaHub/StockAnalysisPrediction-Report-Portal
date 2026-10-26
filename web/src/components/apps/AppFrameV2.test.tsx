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
 act(()=>sessions[0].options.onReady?.(presentation));expect(frame.style.visibility).toBe('visible');expect(frame.hasAttribute('aria-hidden')).toBe(false);expect(screen.queryByRole('status')).toBeNull()
})
test('reload fences the old ready callback and restores a themed cover',()=>{
 render(<AppFrameV2 {...props}/>);const frame=screen.getByTitle('Fixture');fireEvent.load(frame);const old=sessions[0];act(()=>old.options.onReady?.(presentation));fireEvent.load(frame)
 expect(old.close).toHaveBeenCalledTimes(1);expect(frame.style.visibility).toBe('hidden');act(()=>old.options.onReady?.(presentation));act(()=>old.options.onFailure?.('transport_error'));expect(screen.queryByRole('alert')).toBeNull();expect(frame.style.visibility).toBe('hidden');act(()=>sessions[1].options.onReady?.(presentation));expect(frame.style.visibility).toBe('visible')
})
test('presentation updates do not navigate the frame; binding changes replace it',()=>{
 const view=render(<AppFrameV2 {...props}/>);const frame=screen.getByTitle('Fixture');fireEvent.load(frame);act(()=>sessions[0].options.onReady?.(presentation))
 const changed={...presentation,locale:'zh-TW' as const,theme:{...presentation.theme,dark:false,colorBgLayout:'#ffffff'}}
 view.rerender(<AppFrameV2 {...props} presentation={changed}/>);expect(screen.getByTitle('Fixture')).toBe(frame);expect(sessions[0].updatePresentation).toHaveBeenLastCalledWith(changed);expect(frame.style.backgroundColor).toBe('rgb(255, 255, 255)');expect(frame.style.colorScheme).toBe('light')
 view.rerender(<AppFrameV2 {...props} binding={{...binding,generation:'9007199254740994'}}/>);expect(screen.getByTitle('Fixture')).not.toBe(frame);expect(sessions[0].close).toHaveBeenCalledTimes(1);expect(screen.getByTitle('Fixture').style.visibility).toBe('hidden')
})
test('failure removes executable frame and retry creates a new covered document',()=>{
 const view=render(<AppFrameV2 {...props}/>);let frame=screen.getByTitle('Fixture');fireEvent.load(frame);const old=sessions[0];act(()=>old.options.onFailure?.('protocol_error'))
 expect(screen.queryByTitle('Fixture')).toBeNull();expect(screen.getByRole('alert').textContent).toContain('apps.v2Failed');expect(old.close).toHaveBeenCalledTimes(1)
 fireEvent.click(screen.getByRole('button',{name:'apps.v2Retry'}));frame=screen.getByTitle('Fixture');expect(frame.style.visibility).toBe('hidden');act(()=>old.options.onReady?.(presentation));expect(frame.style.visibility).toBe('hidden');fireEvent.load(frame);view.unmount();expect(sessions[1].close).toHaveBeenCalledTimes(1)
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
