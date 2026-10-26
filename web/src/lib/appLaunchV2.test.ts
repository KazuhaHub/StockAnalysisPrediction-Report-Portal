import {expect,test} from 'vitest'
import {parseAppLaunchV2} from './appLaunchV2'
const id='00000000-0000-4000-8000-000000000001'
function fixture(){const binding={installationId:id,generation:'9223372036854775807',policyRevision:'9007199254740993',releaseSha256:'a'.repeat(64)};return {source:`/app-documents/${id}/${binding.generation}/${binding.policyRevision}/${binding.releaseSha256}/${'b'.repeat(64)}/nested/index.html`,binding,capabilities:[],applicationId:'notes-sample',layout:'viewport',timeZone:'UTC'}}
test('exact counters and nested entry survive public launch metadata parsing',()=>{const value=fixture();expect(parseAppLaunchV2(value,id)).toEqual(value)})
test.each(['0','01','+1','9223372036854775808','1e3'])('counter alias or overflow refuses %s',generation=>{const value=fixture();value.binding.generation=generation;expect(parseAppLaunchV2(value,id)).toBeNull()})
test.each(['../index.html','./index.html','nested//index.html','%69ndex.html','index.html?x=1','index.html#x',''])('unsafe entry refuses %s',entry=>{const value=fixture();value.source=value.source.replace('nested/index.html',entry);expect(parseAppLaunchV2(value,id)).toBeNull()})
