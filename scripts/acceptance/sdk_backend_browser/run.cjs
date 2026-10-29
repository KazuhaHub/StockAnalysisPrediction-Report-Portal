// Disposable Linux acceptance only. No production browser profile or credentials.
const {createRequire}=require('node:module');
const {chromium}=createRequire(process.env.SDK_BROWSER_PACKAGE)('playwright');
const origin='http://127.0.0.1:31935';
let stage='launch';
let readFailure='none';
async function main(){
 const browser=await chromium.launch({headless:true,timeout:10000});
 const deadline=setTimeout(()=>{void browser.close()},20000);
 try{
  const context=await browser.newContext({acceptDownloads:false});
  const page=await context.newPage();page.setDefaultTimeout(8000);
  stage='load';await page.goto(origin,{waitUntil:'domcontentloaded'});
  const frame=page.frameLocator('iframe');
  await frame.getByTestId('backend-ready').filter({hasText:'true'}).waitFor();
  const result=frame.getByTestId('backend-result');
  async function read(){await frame.getByRole('button',{name:'Read backend',exact:true}).click();await result.filter({hasText:/"status":200|^error:/}).waitFor();const text=await result.innerText();if(text.startsWith('error:')){readFailure=/^error:(not_ready|unsupported|invalid_request|busy|unavailable|uncertain)$/.test(text)?text:'invalid-result';throw new Error('read refused')}return JSON.parse(text)}
  stage='initial-read';const before=await read();
  if(before.role!=='user'||!/^[a-f0-9]{64}$/.test(before.subject))throw new Error('current normal viewer not observed');
  stage='locale';await page.getByRole('button',{name:'Traditional Chinese',exact:true}).click();
  await frame.getByTestId('locale').filter({hasText:'zh-TW'}).waitFor();
  stage='write';await frame.getByRole('button',{name:'Write backend',exact:true}).click();
  await result.filter({hasText:'"count":'+(before.count+1)}).waitFor();
  const write=JSON.parse(await result.innerText());
  if(write.count!==before.count+1||write.role!=='user'||write.subject!==before.subject||write.calls!==before.calls+1)throw new Error('write identity or call count changed');
  stage='uncertain';await frame.getByRole('button',{name:'Disconnect write',exact:true}).click();
  await result.filter({hasText:'error:uncertain'}).waitFor();
  stage='verify-count';const after=await read();
  if(after.count!==before.count+2||after.calls!==before.calls+3)throw new Error('uncertain browser write retried or omitted');
  stage='revoke';await page.getByRole('button',{name:'Revoke fixture access',exact:true}).click();
  await page.getByTestId('launch-status').filter({hasText:'revoke:204'}).waitFor();
  await frame.getByRole('button',{name:'Read backend',exact:true}).click();
  await result.filter({hasText:'error:unavailable'}).waitFor();
  if(await page.locator('iframe').getAttribute('sandbox')!=='allow-scripts')throw new Error('sandbox weakened');
  await context.close();
  console.log('SDK_NATIVE_BROWSER_CURRENT_VIEWER');
  console.log('SDK_NATIVE_BROWSER_UNCERTAIN_NO_RETRY');
  console.log('SDK_NATIVE_BROWSER_REVOKED');
 }finally{clearTimeout(deadline);await browser.close()}
}
main().catch(()=>{console.error('SDK native browser acceptance failed at '+stage+' ('+readFailure+')');process.exitCode=1});
