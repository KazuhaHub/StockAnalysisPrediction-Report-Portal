// Disposable Linux acceptance only. No production browser profile or credentials.
const {createRequire}=require('node:module');
const {chromium}=createRequire(process.env.SDK_BROWSER_PACKAGE)('playwright');
const origin='http://127.0.0.1:31935';
let stage='launch';
let readFailure='none';
async function main(){
 const browser=await chromium.launch({headless:true,timeout:10000});
 const deadline=setTimeout(()=>{void browser.close()},60000);
 try{
  const context=await browser.newContext({acceptDownloads:false});
  const page=await context.newPage();page.setDefaultTimeout(8000);
  stage='load';await page.goto(origin,{waitUntil:'domcontentloaded'});
  const frame=page.frameLocator('iframe');
  await frame.getByTestId('backend-ready').filter({hasText:'true'}).waitFor();
  stage='theme-refresh';
  let delayedThemeAssets=0;
  await page.route('**/app-documents/**',async route=>{
   delayedThemeAssets++;
   await new Promise(resolve=>setTimeout(resolve,900));
   await route.continue().catch(()=>{});
  });
  async function themeRefresh(mode){
   const expected=mode==='dark'?'rgb(0, 0, 0)':'rgb(245, 245, 245)';
   await page.evaluate(theme=>localStorage.setItem('sdk-fixture-theme',theme),mode);
   await page.reload({waitUntil:'domcontentloaded'});
   await page.locator('.portal-app-frame-v2-cover').waitFor();
   for(let sample=0;sample<18;sample++){
    const colors=await page.evaluate(()=>{
     const host=document.querySelector('[data-testid="host-surface"]');
     const cover=document.querySelector('.portal-app-frame-v2-cover');
     const iframe=document.querySelector('iframe');
     return {host:host&&getComputedStyle(host).backgroundColor,cover:cover?.parentElement&&getComputedStyle(cover.parentElement).backgroundColor,frame:iframe&&getComputedStyle(iframe).backgroundColor,scheme:iframe?.style.colorScheme};
    });
    if(colors.host!==expected||colors.cover!==expected||colors.frame!==expected||colors.scheme!==mode)throw new Error(mode+' loading frame did not retain its initial tokens: '+JSON.stringify(colors));
    await page.waitForTimeout(40);
   }
   await frame.getByTestId('backend-ready').filter({hasText:'true'}).waitFor();
   await frame.getByTestId('dark').filter({hasText:String(mode==='dark')}).waitFor();
   const bodyColors=await frame.locator('body').evaluate(element=>({background:getComputedStyle(element).backgroundColor,colorScheme:document.documentElement.style.colorScheme}));
   if(bodyColors.background!==expected||bodyColors.colorScheme!==mode)throw new Error(mode+' SDK committed mismatched initial tokens: '+JSON.stringify(bodyColors));
  }
  for(const mode of ['dark','light','dark','light','dark'])await themeRefresh(mode);
  if(delayedThemeAssets<5)throw new Error('theme refresh did not delay the application document');
  await page.unroute('**/app-documents/**');
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
  const networkAvailable=await frame.getByTestId('network-ready').innerText()==='true';
  const networkStreamAvailable=await frame.getByTestId('network-stream-ready').innerText()==='true';
  let reportsIssued=0;
  async function dispatchReports(){
   await frame.getByRole('button',{name:'Read reports',exact:true}).click();
   const result=frame.locator('[data-testid="reports-result"][data-request-id="'+(++reportsIssued)+'"]');
   await result.filter({hasNotText:'pending'}).waitFor();
   return result.innerText();
  }
  if(networkAvailable){
   stage='network-success';await frame.getByRole('button',{name:'Read network',exact:true}).click();
   const result=frame.locator('[data-testid="network-result"]');
   await result.filter({hasNotText:'pending'}).waitFor();
   const text=await result.innerText(),value=JSON.parse(text);
   if(value.status!==200||value.contentType!=='application/json'||value.body!=='{"ok":true}')throw new Error('network SDK result changed');
  }
  if(networkStreamAvailable){
   stage='network-stream-success';await frame.getByRole('button',{name:'Read network stream',exact:true}).click();
   const result=frame.locator('[data-testid="network-stream-result"]');
   await result.filter({hasNotText:'pending'}).waitFor({timeout:25000});
   const text=await result.innerText();
   if(text.startsWith('error:'))throw new Error('network stream returned '+text);
   const value=JSON.parse(text);
   if(value.status!==200||value.contentType!=='application/x-ndjson'||value.bytes!==8||value.frames<1||value.body!=='one\\ntwo\\n')throw new Error('network stream SDK result changed');
  }
  const credentialAvailable=await frame.getByTestId('credential-ready').innerText()==='true';
  if(credentialAvailable){
   stage='credential-network';await frame.getByRole('button',{name:'Read credentialed network',exact:true}).click();
   const result=frame.locator('[data-testid="credential-result"]');
   await result.filter({hasNotText:'pending'}).waitFor({timeout:25000});
   const text=await result.innerText();
   if(text.startsWith('error:'))throw new Error('credential request returned '+text);
   const value=JSON.parse(text);
   if(value.status!==200||value.contentType!=='application/json'||value.body!=='{"ok":true}')throw new Error('credential SDK result changed');
  }
  const notificationsReady=await frame.getByTestId('notifications-ready').innerText()==='true';
  if(notificationsReady){
   stage='notifications';await frame.getByRole('button',{name:'Publish notification',exact:true}).click();
   const result=frame.locator('[data-testid="notification-result"]');
   await result.filter({hasNotText:'pending'}).waitFor();
   const text=await result.innerText();
   if(text.startsWith('error:'))throw new Error('notification request returned '+text);
   const value=JSON.parse(text);
   if(!/^[A-Za-z0-9_.:-]{1,128}$/.test(value.id))throw new Error('notification result changed');
  }
  const reportsReady=await frame.getByTestId('reports-ready').innerText();
  if(reportsReady==='true'){
   stage='reports';const reportText=await dispatchReports(),report=JSON.parse(reportText);
   if(report.count!==1||report.total!==1||report.title!=='Browser current viewer report'||!report.bodyMarkdown.includes('Current viewer evidence.'))throw new Error('reports SDK result changed');
  }
  let objectsIssued=0;
  const objectsReady=await frame.getByTestId('objects-ready').innerText();
  async function dispatchObject(name){
   await frame.getByRole('button',{name,exact:true}).click();
   const result=frame.locator('[data-testid="objects-result"][data-request-id="'+(++objectsIssued)+'"]');
   await result.filter({hasNotText:'pending'}).waitFor();
   return result.innerText();
  }
  if(objectsReady==='true'){
   stage='objects';const text=await dispatchObject('Write object'),value=JSON.parse(text);
   if(value.count!==1||value.label!=='Browser note'||value.body!=='Persisted object'||value.revision!==1)throw new Error('object SDK result changed');
  }
  stage='page-actions';await page.getByRole('heading',{name:'Fixture zh-CN /',exact:true}).waitFor();
  await page.getByRole('button',{name:'Header increment',exact:true}).click();
  await frame.getByTestId('page-count').filter({hasText:'1'}).waitFor();
  await page.getByRole('button',{name:'Header wait',exact:true}).click();
  await page.getByRole('button',{name:'Header wait',exact:true,disabled:true}).waitFor();
  stage='locale';await page.getByRole('button',{name:'Traditional Chinese',exact:true}).click();
  await frame.getByTestId('locale').filter({hasText:'zh-TW'}).waitFor();
  stage='navigation';await frame.getByRole('button',{name:'Retained 0',exact:true}).click();
  await frame.getByRole('button',{name:'Open editor',exact:true}).click();
  await page.waitForURL(origin+'/editor');await frame.getByTestId('app-route').filter({hasText:'/editor'}).waitFor();
  await frame.getByRole('button',{name:'Retained 1',exact:true}).waitFor();
  await frame.getByTestId('page-cancelled').filter({hasText:'1'}).waitFor();
  await page.getByRole('heading',{name:'Fixture zh-TW /editor',exact:true}).waitFor();
  await page.goBack();await page.waitForURL(origin+'/');await frame.getByTestId('app-route').filter({hasText:'/',hasNotText:'editor'}).waitFor();
  await page.goForward();await page.waitForURL(origin+'/editor');await frame.getByTestId('app-route').filter({hasText:'/editor'}).waitFor();
  await frame.getByRole('button',{name:'Retained 1',exact:true}).waitFor();
  await page.waitForTimeout(110);
  await frame.getByRole('button',{name:'Open home',exact:true}).click();await page.waitForURL(origin+'/');
  await frame.getByTestId('app-route').filter({hasText:'/',hasNotText:'editor'}).waitFor();
  stage='query-state';await page.waitForTimeout(110);await frame.getByRole('button',{name:'Set filters',exact:true}).click();
  const search='?active=false&page=2&q=%E4%B8%AD%E6%96%87+%26+test&sort=newest';
  await page.waitForURL(origin+'/'+search);await frame.getByTestId('app-query').filter({hasText:'"page":2'}).waitFor();
  const query=JSON.parse(await frame.getByTestId('app-query').innerText());
  if(query.page!==2||query.active!==false||query.q!=='中文 & test'||query.sort!=='newest')throw new Error('typed query lost');
  await page.goBack();await page.waitForURL(origin+'/');await frame.getByTestId('app-query').filter({hasText:'{}'}).waitFor();
  await page.goForward();await page.waitForURL(origin+'/'+search);await frame.getByTestId('app-query').filter({hasText:'"page":2'}).waitFor();
  await frame.getByRole('button',{name:'Retained 1',exact:true}).waitFor();
  stage='query-reload';await page.reload({waitUntil:'domcontentloaded'});issued=0;reportsIssued=0;objectsIssued=0;
  await frame.getByTestId('backend-ready').filter({hasText:'true'}).waitFor();
  await frame.getByTestId('app-query').filter({hasText:'"page":2'}).waitFor();
  if(objectsReady==='true'){
   await frame.getByTestId('objects-ready').filter({hasText:'true'}).waitFor();
   stage='objects-reload';const text=await dispatchObject('Read object'),value=JSON.parse(text);
   if(value.body!=='Persisted object')throw new Error('object did not persist across reload');
  }
  const restored=JSON.parse(await frame.getByTestId('app-query').innerText());
  if(restored.page!==2||restored.active!==false||restored.q!=='中文 & test'||restored.sort!=='newest')throw new Error('query reload lost typed values');
  await frame.getByRole('button',{name:'Retained 0',exact:true}).waitFor();
  const effects=await frame.getByTestId('query-effects').innerText();
  await page.getByRole('button',{name:'Traditional Chinese',exact:true}).click();await frame.getByTestId('locale').filter({hasText:'zh-TW'}).waitFor();
  if(await frame.getByTestId('query-effects').innerText()!==effects)throw new Error('appearance repeated query effects');
  await page.waitForTimeout(110);await frame.getByRole('button',{name:'Open home',exact:true}).click();await page.waitForURL(origin+'/');
  await frame.getByTestId('app-query').filter({hasText:'{}'}).waitFor();
  stage='page-wrap';await frame.getByRole('button',{name:'Toggle long labels',exact:true}).click();
  const longLabel='L'.repeat(48);await page.getByRole('button',{name:longLabel,exact:true}).waitFor();
  await page.setViewportSize({width:320,height:800});
  const geometry=await page.evaluate(()=>({scroll:document.documentElement.scrollWidth,width:window.innerWidth}));
  if(geometry.scroll>geometry.width+1)throw new Error('shared page header overflows narrow viewport');
  await page.getByRole('button',{name:longLabel,exact:true}).focus();
  await page.getByRole('button',{name:longLabel,exact:true}).press('Enter');
  await frame.getByTestId('page-count').filter({hasText:'1'}).waitFor();
  await page.setViewportSize({width:1280,height:720});
  await frame.getByRole('button',{name:'Toggle long labels',exact:true}).click();
  stage='layout-grow';
  const collapsedHeight=await page.evaluate(()=>document.querySelector('iframe')?.getBoundingClientRect().height||0);
  await frame.getByRole('button',{name:'Toggle long content',exact:true}).click();
  await page.waitForFunction((previous)=>{const height=document.querySelector('iframe')?.getBoundingClientRect().height||0;return height>previous+2500},collapsedHeight);
  const expandedHeight=await page.evaluate(()=>document.querySelector('iframe')?.getBoundingClientRect().height||0);
  const inner=await frame.locator('html').evaluate(element=>({scroll:element.scrollHeight,client:element.clientHeight}));
  if(inner.scroll>inner.client+2)throw new Error('document gained internal scroll');
  await frame.getByRole('button',{name:'Toggle long content',exact:true}).click();
  await page.waitForFunction((previous)=>{const height=document.querySelector('iframe')?.getBoundingClientRect().height;return height>=64&&height<previous-2000},expandedHeight);
  stage='viewport-editor';await frame.getByRole('button',{name:'Open editor',exact:true}).click();
  await page.waitForURL(origin+'/editor');
  const editor=frame.getByTestId('viewport-editor');await editor.waitFor();
  const editorGeometry=await editor.evaluate(element=>({scroll:element.scrollHeight,client:element.clientHeight}));
  const documentGeometry=await frame.locator('html').evaluate(element=>({scroll:element.scrollHeight,client:element.clientHeight}));
  if(documentGeometry.scroll>documentGeometry.client+2)throw new Error('viewport layout scrolled the application document');
  if(editorGeometry.scroll<=editorGeometry.client+200)throw new Error('viewport editor did not own its internal scroll');
  await editor.focus();await page.keyboard.press('End');await page.waitForTimeout(50);
  if(await editor.evaluate(element=>element.scrollTop<=0))throw new Error('viewport editor was not keyboard scrollable');
  await frame.getByRole('button',{name:'Open home',exact:true}).click();await page.waitForURL(origin+'/');
  stage='keyboard-form-dialog';
  await frame.getByLabel('Fixture name',{exact:true}).fill('Keyboard draft');
  await page.keyboard.press('Tab');
  if(!await frame.getByRole('button',{name:'Save form',exact:true}).evaluate(element=>element===document.activeElement))throw new Error('form Tab order missed Save form');
  await page.keyboard.press('Enter');await frame.getByTestId('saved-name').filter({hasText:'Keyboard draft'}).waitFor();
  const dialogTrigger=frame.getByRole('button',{name:'Open app dialog',exact:true});
  await dialogTrigger.click();const appDialog=frame.getByRole('dialog',{name:'App dialog'});await appDialog.waitFor();
  if(!await appDialog.getByRole('button',{name:'Confirm app dialog',exact:true}).isVisible())throw new Error('app dialog action was unreachable');
  await page.keyboard.press('Escape');await appDialog.waitFor({state:'hidden'});await page.waitForTimeout(100);
  if(!await dialogTrigger.evaluate(element=>element===document.activeElement))throw new Error('app dialog did not restore focus');
  async function failAndReload(name){
   stage=name;await frame.getByRole('button',{name,exact:true}).click();
   await page.getByRole('alert').filter({hasText:'apps.v2Failed'}).waitFor({timeout:5000});
   if(await page.locator('iframe').count()!==0)throw new Error(name+' left executable content mounted');
   await page.reload({waitUntil:'domcontentloaded'});
   await frame.getByTestId('backend-ready').filter({hasText:'true'}).waitFor();
   issued=0;reportsIssued=0;objectsIssued=0;
  }
  await failAndReload('Malicious shell descriptor');
  await failAndReload('Huge document height');
  stage='oscillating-height';await frame.getByRole('button',{name:'Oscillate height',exact:true}).click();
  await page.waitForTimeout(900);
  if(await page.locator('iframe').count()!==1)throw new Error('oscillating height did not retain the app');
  if(await page.evaluate(()=>document.querySelector('iframe')?.getBoundingClientRect().height||0)>10000)throw new Error('oscillating height grew without a bound');
  await frame.getByTestId('backend-ready').filter({hasText:'true'}).waitFor();
  stage='runaway-render';await frame.getByRole('button',{name:'Runaway render',exact:true}).click();
  await page.waitForTimeout(1200);
  if(await page.locator('iframe').count()!==1)throw new Error('bounded rerender unexpectedly unmounted the host');
  const responsiveStart=Date.now();await page.evaluate(()=>document.body.dataset.boundedRerender='host-responsive');
  if(Date.now()-responsiveStart>1000)throw new Error('bounded rerender blocked the host');
  if(await page.evaluate(()=>document.querySelector('iframe')?.getBoundingClientRect().height||0)>10000)throw new Error('bounded rerender grew the host without a limit');
  await page.reload({waitUntil:'domcontentloaded'});
  await frame.getByTestId('backend-ready').filter({hasText:'true'}).waitFor();
  issued=0;reportsIssued=0;objectsIssued=0;
  stage='write';const write=JSON.parse(await dispatch('Write backend'));
  if(write.status!==200||write.count!==before.count+1||write.role!=='user'||write.subject!==before.subject||write.calls!==before.calls+1)throw new Error('write identity or call count changed');
  stage='uncertain';if(await dispatch('Disconnect write')!=='error:uncertain')throw new Error('missing uncertainty');
  stage='verify-count';const after=await read();
  if(after.count!==before.count+2||after.calls!==before.calls+3)throw new Error('uncertain browser write retried or omitted');
  stage='revoke';await page.getByRole('button',{name:'Revoke fixture access',exact:true}).click();
  await page.getByTestId('launch-status').filter({hasText:'revoke:204'}).waitFor();
  if(await dispatch('Read backend')!=='error:unavailable')throw new Error('revoked backend remained available');
  if(networkAvailable){
   stage='network-revoked';await frame.getByRole('button',{name:'Read network',exact:true}).click();
   const result=frame.locator('[data-testid="network-result"]');
   await result.filter({hasNotText:'pending'}).waitFor();
   if(await result.innerText()!=='error:unavailable')throw new Error('revoked network remained available');
  }
  if(networkStreamAvailable){
   stage='network-stream-revoked';await frame.getByRole('button',{name:'Read network stream',exact:true}).click();
   const result=frame.locator('[data-testid="network-stream-result"]');
   await result.filter({hasNotText:'pending'}).waitFor();
   if(await result.innerText()!=='error:unavailable')throw new Error('revoked network stream remained available');
  }
  if(reportsReady==='true'){
   stage='reports-revoked';const revokedReport=await dispatchReports();
   if(revokedReport!=='error:unavailable')throw new Error('revoked reports returned '+revokedReport);
  }
  if(objectsReady==='true'){
   stage='objects-revoked';const revokedObject=await dispatchObject('Read object');
   if(revokedObject!=='error:unavailable')throw new Error('revoked objects returned '+revokedObject);
  }
  if(await page.locator('iframe').getAttribute('sandbox')!=='allow-scripts')throw new Error('sandbox weakened');
  stage='draft-edit';await frame.getByRole('button',{name:'Edit draft',exact:true}).click();
  await frame.getByRole('button',{name:'Save draft',exact:true}).waitFor();
  // The boolean hint is deliberately coalesced; wait for its bounded flush.
  await page.waitForTimeout(150);
  const draftEffects=await frame.getByTestId('query-effects').innerText();
  stage='draft-presentation';
  await page.getByRole('button',{name:'Toggle host theme',exact:true}).click();
  await frame.getByTestId('dark').filter({hasText:'false'}).waitFor();
  await page.getByRole('button',{name:'Set Taipei time',exact:true}).click();
  await frame.getByTestId('time-zone').filter({hasText:'Asia/Taipei'}).waitFor();
  await page.getByRole('button',{name:'Set English',exact:true}).click();
  await frame.getByTestId('locale').filter({hasText:'en-US'}).waitFor();
  await frame.getByRole('button',{name:'Save draft',exact:true}).waitFor();
  if(await frame.getByTestId('query-effects').innerText()!==draftEffects)throw new Error('presentation change replayed query effects');
  if(await frame.getByRole('combobox').count())throw new Error('app exposed an independent language picker');
  console.log('SDK_NATIVE_BROWSER_PRESENTATION_DRAFT');
  stage='draft-cancel';await page.getByRole('link',{name:'Leave fixture',exact:true}).click();
  const dialog=page.getByRole('dialog');await dialog.waitFor();
  await dialog.getByRole('button',{name:'apps.draftStay',exact:true}).click();
  await dialog.waitFor({state:'hidden'});
  await frame.getByRole('button',{name:'Save draft',exact:true}).waitFor();
  stage='draft-confirm';await page.getByRole('link',{name:'Leave fixture',exact:true}).click();
  await dialog.getByRole('button',{name:'apps.draftLeave',exact:true}).click();
  await page.getByText('Fixture closed',{exact:true}).waitFor();
  if(await page.locator('iframe').count()!==0)throw new Error('retired app remained mounted');
  await context.close();
  console.log('SDK_NATIVE_BROWSER_CURRENT_VIEWER');
  console.log('SDK_NATIVE_BROWSER_THEME_REFRESH');
  console.log('SDK_NATIVE_BROWSER_DOCUMENT_LAYOUT');
  console.log('SDK_NATIVE_BROWSER_VIEWPORT_EDITOR');
  console.log('SDK_NATIVE_BROWSER_KEYBOARD_FORM_DIALOG');
  console.log('SDK_NATIVE_BROWSER_BOUNDED_SHELL_RENDER');
  console.log('SDK_NATIVE_BROWSER_DRAFT_GUARD');
  console.log('SDK_NATIVE_BROWSER_NAVIGATION');
  console.log('SDK_NATIVE_BROWSER_TYPED_QUERY');
  console.log('SDK_NATIVE_BROWSER_PAGE_ACTIONS');
  console.log('SDK_NATIVE_BROWSER_UNCERTAIN_NO_RETRY');
  console.log('SDK_NATIVE_BROWSER_REVOKED');
  if(networkAvailable){
   console.log('SDK_NATIVE_BROWSER_NETWORK');
  }
  if(networkStreamAvailable){
   console.log('SDK_NATIVE_BROWSER_NETWORK_STREAM');
  }
  if(credentialAvailable){
   console.log('SDK_NATIVE_BROWSER_CREDENTIAL');
  }
  if(notificationsReady){
   console.log('SDK_NATIVE_BROWSER_NOTIFICATIONS');
  }
  if(reportsReady==='true'){
   console.log('SDK_NATIVE_BROWSER_REPORTS');
  }
  if(objectsReady==='true'){
   console.log('SDK_NATIVE_BROWSER_OBJECTS');
  }
}finally{clearTimeout(deadline);await browser.close()}
}
main().catch(error=>{console.error('SDK native browser acceptance failed at '+stage+' ('+readFailure+'): '+(error?.stack||error));process.exitCode=1});
