import { expect, test } from "vitest";
import { AppNetworkError, encodeNetworkBody, decodeNetworkBody, parseNetworkRequest, parseNetworkResponse } from "./network-v2";
const request = () => ({method:"POST",url:"https://api.example.com/v1/check?q=%E4%B8%AD",accept:"application/json",contentType:"application/octet-stream",bodyBase64:"AP+A"});
test("network bytes round trip exactly at both fixed limits",()=>{
 const body=new Uint8Array(65536);for(let i=0;i<body.length;i++)body[i]=i%256;
 const wire=encodeNetworkBody(body);expect(decodeNetworkBody(wire)).toEqual(body);
 expect(parseNetworkRequest({...request(),bodyBase64:wire})).toEqual({...request(),bodyBase64:wire});
 const response="A".repeat(1398100)+"AA==";
 expect(decodeNetworkBody(response).length).toBe(1048576);
 expect(parseNetworkResponse({status:503,contentType:"",bodyBase64:response})).not.toBeNull();
 expect(()=>encodeNetworkBody(new Uint8Array(65537))).toThrow(AppNetworkError);
 expect(parseNetworkRequest({...request(),bodyBase64:encodeNetworkBody(new Uint8Array(65535))+"AAAA"})).toBeNull();
});
test("network wire rejects ambient authority and malformed inputs",()=>{
 for(const bad of [null,[],{...request(),headers:{}},{...request(),credential:{}},{...request(),credential:"Bad"},{...request(),credential:"upstream-api",credentialValue:"secret"},{...request(),method:"CONNECT"},{...request(),url:"http://api.example.com/v1"},{...request(),url:"https://u:p@api.example.com/v1"},{...request(),url:"https://api.example.com/v1#"},{...request(),url:"https://api.example.com/\\evil"},{...request(),accept:"x\r\nCookie: injected"},{...request(),contentType:"a".repeat(257)},{...request(),bodyBase64:"YQ"},{...request(),bodyBase64:"YR=="},{...request(),bodyBase64:"YQ==\n"}])expect(parseNetworkRequest(bad)).toBeNull();
 expect(parseNetworkRequest({...request(),credential:"upstream-api"})).toEqual({...request(),credential:"upstream-api"});
 for(const bad of [{status:302,contentType:"",bodyBase64:""},{status:199,contentType:"",bodyBase64:""},{status:600,contentType:"",bodyBase64:""},{status:200,contentType:"x\n",bodyBase64:""},{status:200,contentType:"",bodyBase64:"",headers:{}},{status:200,contentType:"",bodyBase64:"YQ"}])expect(parseNetworkResponse(bad)).toBeNull();
});
test("network results are copied, empty bodies allowed and failures never retry",()=>{
 const value=request(),copy=parseNetworkRequest(value);value.url="https://other.example/";
 expect(copy?.url).toContain("api.example.com");expect(Object.isFrozen(copy)).toBe(true);
 expect(parseNetworkResponse({status:204,contentType:"",bodyBase64:""})).toEqual({status:204,contentType:"",bodyBase64:""});
 const one=decodeNetworkBody("AP+A");one[0]=42;expect(decodeNetworkBody("AP+A")).toEqual(new Uint8Array([0,255,128]));
 const error=new AppNetworkError("uncertain");expect(error.retryable).toBe(false);expect(error.message).not.toContain("api.example.com");
});
