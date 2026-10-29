// Actual generated frontend in the real Portal shell, on a disposable worker.
const {createRequire}=require('node:module');
const {chromium}=createRequire(process.env.SDK_BROWSER_PACKAGE)('playwright');
let stage='start';
async function main(){
 const browser=await chromium.launch({headless:true,timeout:10000});
 const deadline=setTimeout(()=>void browser.close(),30000);
 try{
  const context=await browser.newContext({locale:'en-US',colorScheme:'light'});
  const page=await context.newPage();page.setDefaultTimeout(8000);
  let backendCalls=0;
  page.on('request',request=>{if(/\/api\/app-platform\/installations\/[^/]+\/backend$/.test(new URL(request.url()).pathname))backendCalls++});
  await page.goto('http://127.0.0.1:31935/__template_fixture/start',{waitUntil:'domcontentloaded'});
  const base=page.url(),frame=page.frameLocator('iframe');
  await page.getByRole('button',{name:'Open editor',exact:true}).waitFor();
  stage='open';await page.getByRole('button',{name:'Open editor',exact:true}).click();
  await page.waitForURL(base+'/editor');
  const field=frame.getByRole('textbox',{name:'Draft text',exact:true});await field.fill('A retained browser draft');
  await page.getByRole('button',{name:'Preview',exact:true}).click();
  await frame.getByRole('status',{name:'Preview',exact:true}).filter({hasText:'A retained browser draft'}).waitFor();
  if(await frame.getByRole('heading').count()!==0||await frame.getByRole('combobox').count()!==0)throw new Error('duplicate app shell');
  stage='draft-cancel';await page.waitForTimeout(150);
  await page.getByRole('button',{name:'Home',exact:true}).click();
  const dialog=page.getByRole('dialog');await dialog.waitFor();
  await dialog.getByRole('button',{name:'Keep editing',exact:true}).click();
  await dialog.waitFor({state:'hidden'});
  if(await field.inputValue()!=='A retained browser draft'||page.url()!==base+'/editor')throw new Error('cancel lost draft');
  stage='draft-confirm';await page.waitForTimeout(110);await page.getByRole('button',{name:'Home',exact:true}).click();
  await dialog.getByRole('button',{name:'Leave app',exact:true}).click();await page.waitForURL(base);await dialog.waitFor({state:'hidden'});await page.getByRole('button',{name:'Open editor',exact:true}).waitFor();
  stage='history';await page.goBack();await dialog.waitFor();
  await dialog.getByRole('button',{name:'Leave app',exact:true}).click();await page.waitForURL(base+'/editor');
  await dialog.waitFor({state:'hidden'});
  if(await field.inputValue()!=='A retained browser draft')throw new Error('outer shell remounted app');
  if(await page.locator('iframe').getAttribute('sandbox')!=='allow-scripts'||backendCalls!==0)throw new Error('starter authority changed');
  stage='proof';const proof=await page.screenshot({fullPage:false});
  let unexpectedDialog=false;page.on('dialog',async dialog=>{if(dialog.type()==='beforeunload')await dialog.accept();else{unexpectedDialog=true;await dialog.dismiss()}});
  stage='refresh';await page.reload({waitUntil:'domcontentloaded'});
  await frame.getByRole('textbox',{name:'Draft text',exact:true}).waitFor();
  if(await frame.getByRole('textbox',{name:'Draft text',exact:true}).inputValue()!=='')throw new Error('unexpected persistence');
  if(unexpectedDialog||backendCalls!==0)throw new Error('unexpected starter side effect');
  await context.close();await require('node:fs/promises').writeFile(process.env.APP_V2_TEMPLATE_PROOF,proof);console.log('V2_TEMPLATE_BROWSER_ACCEPTED');
 }finally{clearTimeout(deadline);await browser.close()}
}
main().catch(()=>{console.error('Generated-template browser failed at '+stage);process.exitCode=1});
