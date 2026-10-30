// Run against card_visual_host.mjs using an installed Playwright and isolated browser.
import assert from 'node:assert/strict';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE||'playwright');
const browser=await chromium.launch({headless:true,...(process.env.CHROME_PATH?{executablePath:process.env.CHROME_PATH}:{})});
try{
 const page=await browser.newPage({viewport:{width:1100,height:1000}});
 // Fixtures only: deny external requests.
 await page.route('**/*',route=>new URL(route.request().url()).hostname==='127.0.0.1'?route.continue():route.abort());
 await page.goto(process.env.CARD_HOST_URL);
 await page.selectOption('#state','IMPLEMENTATION_RUNNING');
 const frame=page.frameLocator('#card');
 await frame.locator('.db-feature-menu > summary').click();await frame.getByRole('button',{name:'Card 设置',exact:true}).click();
 assert.equal(await frame.getByRole('switch').isChecked(),false);
 for(const width of ['320','560','850'])for(const theme of ['light','dark']){
  await page.selectOption('#width',width);await page.selectOption('#theme',theme);
  const result=await frame.locator('.db-card-settings').evaluate(e=>{const r=e.getBoundingClientRect(),d=e.ownerDocument;return {left:r.left,right:r.right,width:d.documentElement.clientWidth,overflow:d.documentElement.scrollWidth>d.documentElement.clientWidth,color:getComputedStyle(e).color,background:getComputedStyle(e.closest('.db-feature-menu-content')).backgroundColor};});
  assert.equal(result.overflow,false);assert.ok(result.left>=0&&result.right<=result.width,JSON.stringify(result));assert.notEqual(result.color,result.background);
  await page.screenshot({path:`/tmp/card-recall-${width}-${theme}.png`});console.log(width,theme,result);
 }
 await frame.getByRole('switch').check();
 await page.evaluate(()=>{window.recallCount=0;addEventListener('message',e=>{if(e.data?.method==='ui/dev-bridge/recall-presentation')window.recallCount++;});document.body.style.paddingBottom='2400px';scrollTo(0,document.body.scrollHeight);});
 await page.waitForFunction(()=>window.recallCount===1,{},{timeout:16000});
 assert.equal(await page.locator('iframe').count(),1);
 assert.equal(await frame.getByRole('switch').isChecked(),true);
 await page.selectOption('#state','VERIFICATION_RUNNING');
 await frame.locator('.db-feature-menu > summary').click();await frame.getByRole('button',{name:'Card 设置',exact:true}).click();assert.equal(await frame.getByRole('switch').isChecked(),true);
 console.log('Host viewport recall, same iframe, preference remount: PASS');
}finally{await browser.close();}
