import { MessageChannel as NodeMessageChannel } from 'node:worker_threads'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import { createBridgeClient, type BridgePresentation } from '../../lib/bridge-v2'
import AppFrameV2 from './AppFrameV2'
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals()})
const binding={installationId:'00000000-0000-0000-0000-000000000001',generation:'9007199254740993',policyRevision:'1',releaseSha256:'a'.repeat(64)}
const presentation:BridgePresentation={locale:'zh-CN',timeZone:'Asia/Shanghai',layout:'document',theme:{dark:true,colorPrimary:'#1677ff',colorBg:'#141414',colorText:'#ffffff',colorBorder:'#424242',colorBgLayout:'#000000',borderRadius:8}}

// jsdom does not transfer MessagePorts between its Window objects. Explicit
// delivery adapters preserve the exact window/origin/port inputs while the real
// shared protocol runs over transferable Node ports. This is not browser evidence.
function delivery(target:Window,source:Window,origin:string,data:unknown,ports:MessagePort[]){
 const transferred=structuredClone({data,ports},{transfer:ports})
 const event=new Event('message')
 Object.defineProperties(event,{source:{value:source},origin:{value:origin},data:{value:transferred.data},ports:{value:transferred.ports}})
 queueMicrotask(()=>target.dispatchEvent(event))
}
test('actual host keeps frame hidden through delayed apply, latest settings and a new load handshake',async()=>{
 vi.stubGlobal('MessageChannel',NodeMessageChannel)
 const draft=vi.fn()
 const props={onDraftChange:draft,source:'/app-assets/fixture/index.html',title:'Fixture',height:640,binding,presentation,capabilities:[]}
 const view=render(<AppFrameV2 {...props}/>);const frame=screen.getByTitle<HTMLIFrameElement>('Fixture');const child=frame.contentWindow!
 vi.spyOn(child,'postMessage').mockImplementation((data:unknown,_target?:unknown,ports?:unknown)=>delivery(child,window,window.location.origin,data,(ports??[]) as MessagePort[]))
 vi.spyOn(window,'postMessage').mockImplementation((data:unknown,_target?:unknown,ports?:unknown)=>delivery(window,child,'null',data,(ports??[]) as MessagePort[]))
 let finish:(()=>void)|undefined
 const applied:BridgePresentation[]=[]
 const client=createBridgeClient({localWindow:child,parentWindow:window,parentOrigin:window.location.origin,onPresentation:next=>{applied.push(next);return new Promise<void>(resolve=>{finish=resolve})}})
 try{
  fireEvent.load(frame);await waitFor(()=>expect(applied).toHaveLength(1));expect(frame.style.visibility).toBe('hidden')
  const changed:BridgePresentation={...presentation,locale:'zh-TW',timeZone:'Asia/Taipei'}
  view.rerender(<AppFrameV2 {...props} presentation={changed}/>);await act(async()=>finish!());await waitFor(()=>expect(applied).toHaveLength(2));expect(frame.style.visibility).toBe('hidden')
  await act(async()=>finish!());await waitFor(()=>expect(client.state()).toBe('ready'));expect(frame.style.visibility).toBe('hidden');act(()=>{expect(client.reportDocumentHeight(320)).toBe(true)});await waitFor(()=>expect(frame.style.visibility).toBe('visible'));expect(client.context()?.binding.generation).toBe('9007199254740993');expect(screen.queryByRole('status')).toBeNull();act(()=>{expect(client.setDraftDirty(true)).toBe(true)});await waitFor(()=>expect(draft).toHaveBeenLastCalledWith(true))
  fireEvent.load(frame);expect(draft).toHaveBeenLastCalledWith(false);expect(frame.style.visibility).toBe('hidden');await waitFor(()=>expect(applied).toHaveLength(3));await act(async()=>finish!());await waitFor(()=>expect(client.state()).toBe('ready'));expect(frame.style.visibility).toBe('hidden');act(()=>{expect(client.reportDocumentHeight(180)).toBe(true)});await waitFor(()=>expect(frame.style.visibility).toBe('visible'))
 }finally{client.close();view.unmount();expect(draft).toHaveBeenLastCalledWith(false)}
})
