import {parseAppDisplay} from './appDisplayV2'
import type {AppDisplay} from './descriptor-contract.generated'
import {DESCRIPTOR_PATTERNS} from './descriptor-contract.generated'
import {parseBridgeRoutes,type BridgeRoute,type BridgeBinding,type BridgePresentation} from './bridge-v2'
export interface AppLaunchV2 {routes:readonly BridgeRoute[];backendAvailable:boolean;display?:AppDisplay;source:string;binding:BridgeBinding;capabilities:readonly string[];applicationId:string;layout:BridgePresentation['layout'];timeZone:string}
const hostCapabilities=new Set(['network.http'])
const uuid=/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/
const digest=new RegExp(DESCRIPTOR_PATTERNS.digest)
function counter(value:unknown):value is string{return typeof value==='string'&&/^[1-9][0-9]{0,18}$/.test(value)&&BigInt(value)<=9223372036854775807n}
export function validInstallationId(value:string):boolean{return uuid.test(value)}
// This validates server metadata for display. Every document and future broker
// request still requires current server authorization; this is not an access grant.
export function parseAppLaunchV2(value:unknown,installation:string):AppLaunchV2|null{
 if(!value||typeof value!=='object')return null
 const v=value as Record<string,unknown>,b=v.binding as Record<string,unknown>|undefined
 const routes=parseBridgeRoutes(v.routes)
 if(!routes||routes.find(route=>route.path==='/')?.layout!==v.layout)return null
 const display=v.display==null?null:parseAppDisplay(v.display)
 if(v.display!=null&&!display)return null
 if(!b||typeof b!=='object'||b.installationId!==installation||!uuid.test(installation)||!counter(b.generation)||!counter(b.policyRevision)||typeof b.releaseSha256!=='string'||!digest.test(b.releaseSha256))return null
 if(v.backendAvailable!==undefined&&typeof v.backendAvailable!=='boolean')return null
 if(typeof v.applicationId!=='string'||!new RegExp(DESCRIPTOR_PATTERNS.slug).test(v.applicationId)||(v.layout!=='document'&&v.layout!=='viewport')||typeof v.timeZone!=='string'||v.timeZone.length>64||!v.timeZone||!Array.isArray(v.capabilities)||v.capabilities.length>hostCapabilities.size||new Set(v.capabilities).size!==v.capabilities.length||!v.capabilities.every(capability=>typeof capability==='string'&&hostCapabilities.has(capability)))return null
 try{new Intl.DateTimeFormat('en-US',{timeZone:v.timeZone})}catch{return null}
 if(typeof v.source!=='string'||v.source.length>1024||/[\\\s%?#]/.test(v.source))return null
 const prefix=`/app-documents/${installation}/${b.generation}/${b.policyRevision}/${b.releaseSha256}/`
 if(!v.source.startsWith(prefix))return null
 const tail=v.source.slice(prefix.length),slash=tail.indexOf('/'),frontend=tail.slice(0,slash),entry=tail.slice(slash+1)
 if(slash<0||!digest.test(frontend)||!entry||entry.length>256||!new RegExp(DESCRIPTOR_PATTERNS.path).test(entry)||entry.includes('..')||entry.split('/').includes('.')||entry.includes('//')||entry.startsWith('/')||entry.endsWith('/'))return null
 return {routes,backendAvailable:v.backendAvailable===true,...(display?{display}:{}),source:v.source,binding:{installationId:installation,generation:b.generation,policyRevision:b.policyRevision,releaseSha256:b.releaseSha256},capabilities:[...v.capabilities],applicationId:v.applicationId,layout:v.layout,timeZone:v.timeZone}
}
