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
  let issued=0;
  async function dispatch(name){
   await frame.getByRole('button',{name,exact:true}).click();
   const result=frame.locator('[data-testid="backend-result"][data-request-id="'+(++issued)+'"]');
   await result.filter({hasNotText:'pending'}).waitFor();
   return result.innerText();
  }
  async function read(){const text=await dispatch('Read backend');if(text.startsWith('error:')){readFailure=/^error:(not_ready|unsupported|invalid_request|busy|unavailable|uncertain)$/.test(text)?text:'invalid-result';throw new Error('read refused')}const value=JSON.parse(text);if(value.status!==200)throw new Error('app read refused');return value}
  stage='initial-read';const before=await read();
  if(before.role!=='user'||!/^[a-f0-9]{64}$/.test(before.subject))throw new Error('current normal viewer not observed');
  stage='locale';await page.getByRole('button',{name:'Traditional Chinese',exact:true}).click();
  await frame.getByTestId('locale').filter({hasText:'zh-TW'}).waitFor();
  stage='layout-grow';
  await frame.getByRole('button',{name:'Toggle long content',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('iframe')?.getBoundingClientRect().height>3000);
  const inner=await frame.locator('html').evaluate(element=>({scroll:element.scrollHeight,client:element.clientHeight}));
  if(inner.scroll>inner.client+2)throw new Error('document gained internal scroll');
  await frame.getByRole('button',{name:'Toggle long content',exact:true}).click();
  await page.waitForFunction(()=>{const height=document.querySelector('iframe')?.getBoundingClientRect().height;return height>=64&&height<1000});
  stage='write';const write=JSON.parse(await dispatch('Write backend'));
  if(write.status!==200||write.count!==before.count+1||write.role!=='user'||write.subject!==before.subject||write.calls!==before.calls+1)throw new Error('write identity or call count changed');
  stage='uncertain';if(await dispatch('Disconnect write')!=='error:uncertain')throw new Error('missing uncertainty');
  stage='verify-count';const after=await read();
  if(after.count!==before.count+2||after.calls!==before.calls+3)throw new Error('uncertain browser write retried or omitted');
  stage='revoke';await page.getByRole('button',{name:'Revoke fixture access',exact:true}).click();
  await page.getByTestId('launch-status').filter({hasText:'revoke:204'}).waitFor();
  if(await dispatch('Read backend')!=='error:unavailable')throw new Error('revoked backend remained available');
  if(await page.locator('iframe').getAttribute('sandbox')!=='allow-scripts')throw new Error('sandbox weakened');
  await context.close();
  console.log('SDK_NATIVE_BROWSER_CURRENT_VIEWER');
  console.log('SDK_NATIVE_BROWSER_DOCUMENT_LAYOUT');
  console.log('SDK_NATIVE_BROWSER_UNCERTAIN_NO_RETRY');
  console.log('SDK_NATIVE_BROWSER_REVOKED');
 }finally{clearTimeout(deadline);await browser.close()}
}
main().catch(()=>{console.error('SDK native browser acceptance failed at '+stage+' ('+readFailure+')');process.exitCode=1});
