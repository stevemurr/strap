// Browser regression checks against a fixture API; no model or container calls.
// NODE_PATH=/path/to/node_modules node scripts/test-eval-web.cjs
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const ui = path.join(__dirname, '../internal/evalweb/ui');
const model = {backend:'vllm',base_url:'http://model:8355/v1',model:'Qwen3.6',timeout_ns:3600e9,generation:{temperature:0.6,enable_thinking:true,preserve_thinking:true}};
const runs = [
 {path:'old-folder-1',name:'old-folder-1',display_name:'Thinking baseline',kind:'ladder',started_at:'2026-09-21T18:00:00Z',commit:'abc1234',branch:'feature/evals',model:'Qwen3.6',configuration:model,tasks:2,passed:1,archived:false},
 {path:'old-folder-2',display_name:'Greedy comparison',kind:'ladder',started_at:'2026-09-20T18:00:00Z',commit:'def5678',branch:'main',model:'Qwen3.6',configuration:{...model,generation:{temperature:0,enable_thinking:false}},tasks:2,passed:2,archived:false},
 {path:'incomplete',display_name:'Interrupted attempt',kind:'ladder',started_at:'2026-09-19T18:00:00Z',tasks:0,passed:0,archived:true,archive_reason:'No readable final results'},
];
const tasks = [{id:'easy-01',title:'Budget pair',tier:'easy',phase:'running',model_calls:12,tool_calls:8,tool_errors:2,input_tokens:42000,output_tokens:1800,started_at:'2026-09-21T18:00:00Z',context:{root:{revision:4,tokens:8192},worker:{revision:2,tokens:4096}}},{id:'easy-02',title:'Window count',tier:'easy',phase:'queued'}];
const snap={id:'job1',name:'UI baseline',dir:'eval-123',status:'running',started_at:'2026-09-21T18:00:00Z',metadata:{model},tasks:[...tasks,...Array.from({length:18},(_,i)=>({id:`queued-${i}`,title:`Additional problem ${i+1}`,phase:'queued'}))]};
const events=[
 {seq:1,task:'easy-01',kind:'agent',agent:'root',text:'started'},
 {seq:2,task:'easy-01',kind:'agent',agent:'worker',parent:'root',text:'started'},
 {seq:3,task:'easy-01',kind:'commentary',agent:'worker',text:'**Plan**\n```go\nfmt.Println("hello")\n```\n<img src=x onerror="window.pwned=1">'},
 {seq:5,task:'easy-01',kind:'agent',agent:'reviewer',parent:'root',text:'started'},
 {seq:6,task:'easy-01',kind:'commentary',agent:'root',text:'Root resumes after delegation'},
 {seq:7,kind:'build',phase:'building',text:'Step 1: compiling image'},
 {seq:8,kind:'build',phase:'ready',text:'Container image ready: strap-eval'},
 {seq:4,task:'easy-02',kind:'commentary',agent:'root',text:'Only second problem'},
];
for(let i=0;i<23;i++)runs.push({path:`older-${i}`,display_name:`Older evaluation ${i+1}`,kind:'ladder',started_at:'2026-08-01T18:00:00Z',model:'Reference',tasks:20,passed:15,status:'done'});
let submitted,buildReady=false;
const streams=new Set();
const server=http.createServer((req,res)=>{
 const url=new URL(req.url,'http://localhost');
 const json=value=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(value));};
 if(url.pathname==='/api/runs')return json({root:'/fixture/results',runs});
 if(url.pathname==='/api/runner')return json({enabled:true,configuration:model,model:model.model,backend:model.backend,base_url:model.base_url,roles:{},tasks});
 if(url.pathname==='/api/jobs'&&req.method==='POST') {let body='';req.on('data',b=>body+=b);req.on('end',()=>{submitted=JSON.parse(body);json(snap);});return;}
 if(url.pathname==='/api/jobs')return json({jobs:[]});
 if(url.pathname==='/api/jobs/job1')return json(snap);
 if(url.pathname==='/api/jobs/job1/events') {
  res.writeHead(200,{'Content-Type':'text/event-stream'});
  streams.add(res);
  for(const e of events.filter(e=>e.phase!=='ready'||buildReady))res.write(`event: progress\ndata: ${JSON.stringify({...e,at:'2026-09-21T18:00:00Z'})}\n\n`);
  const timer=setInterval(()=>res.write(': keepalive\n\n'),1000);req.on('close',()=>{clearInterval(timer);streams.delete(res);});return;
 }
 if(url.pathname==='/api/library') {let body='';req.on('data',b=>body+=b);req.on('end',()=>{const edit=JSON.parse(body);const run=runs.find(r=>r.path===edit.path);if(edit.name)run.display_name=edit.name;if(edit.delete)runs.splice(runs.indexOf(run),1);json(edit);});return;}
 const file=path.join(ui,url.pathname==='/'?'index.html':url.pathname.slice(1));
 if(!file.startsWith(ui+path.sep)||!fs.existsSync(file)){res.writeHead(404);res.end();return;}
 res.setHeader('Content-Type',file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':'text/html');res.end(fs.readFileSync(file));
});
(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true});
 const page=await browser.newPage({viewport:{width:1440,height:1000}});
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 try {
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.getByRole('link',{name:'Thinking baseline',exact:true}).waitFor();
  assert.equal(await page.getByRole('link',{name:'Archive',exact:true}).count(),0);
  assert.equal(await page.locator('#sidebar').getByRole('link',{name:'New eval',exact:true}).count(),0);
  await page.getByLabel('Branch',{exact:true}).selectOption('@latest');
  assert.equal(await page.getByRole('link',{name:'Greedy comparison',exact:true}).count(),0);
  await page.getByRole('button',{name:'Clear filters'}).click();
  await page.getByLabel('Commit (newest run first)').selectOption('def5678');
  assert.equal(await page.getByRole('link',{name:'Thinking baseline',exact:true}).count(),0);
  await page.getByRole('button',{name:'Clear filters'}).click();
  await page.getByLabel('Thinking',{exact:true}).selectOption('false');
  assert.equal(await page.getByRole('link',{name:'Greedy comparison',exact:true}).count(),1);
  await page.getByRole('button',{name:'Clear filters'}).click();
  await page.screenshot({path:'/tmp/strap-eval-library.png',fullPage:true,animations:'disabled'});
  await page.getByLabel('Rows per page',{exact:true}).selectOption('5');
  assert.equal(await page.locator('.library-table tbody tr').count(),5);
  await page.getByRole('button',{name:'Next page',exact:true}).click();
  assert.equal(await page.getByRole('link',{name:'Thinking baseline',exact:true}).count(),0);
  await page.getByLabel('Model',{exact:true}).selectOption('Qwen3.6');
  assert.equal(await page.getByRole('button',{name:'Previous page',exact:true}).isDisabled(),true);
  await page.getByRole('button',{name:'Clear filters'}).click();
  await page.locator('tr').filter({hasText:'Interrupted attempt'}).locator('.row-menu > summary').click();
  page.once('dialog',dialog=>dialog.accept());
  await page.locator('tr').filter({hasText:'Interrupted attempt'}).getByRole('button',{name:'Delete',exact:true}).click();
  await page.waitForFunction(()=>!document.body.textContent.includes('Interrupted attempt'));
  await page.getByRole('link',{name:'Settings',exact:true}).click();
  assert.equal(await page.getByLabel('Results path',{exact:true}).inputValue(),'/fixture/results');
  await page.getByLabel('Appearance',{exact:true}).selectOption('dark');
  await page.getByLabel('Default rows per page',{exact:true}).selectOption('20');
  await page.screenshot({path:'/tmp/strap-eval-settings.png',fullPage:true,animations:'disabled'});
  await page.getByRole('link',{name:'Evaluations',exact:true}).click();
  await page.locator('main').getByRole('heading',{name:'Evaluations',exact:true}).waitFor();
  assert.equal(await page.locator('.library-table tbody tr').count(),20);
  await page.getByLabel('Rows per page',{exact:true}).selectOption('10');
  await page.screenshot({path:'/tmp/strap-eval-library-new.png',fullPage:true,animations:'disabled'});
  await page.locator('tr').filter({hasText:'Thinking baseline'}).locator('.row-menu > summary').click();
  page.once('dialog',dialog=>dialog.accept('Renamed experiment'));
  await page.locator('tr').filter({hasText:'Thinking baseline'}).getByRole('button',{name:'Rename'}).click();
  await page.getByRole('link',{name:'Renamed experiment',exact:true}).waitFor();
  await page.getByRole('link',{name:'New eval',exact:true}).first().click();
  assert.equal(await page.getByLabel('Preserve thinking',{exact:true}).inputValue(),'true');
  await page.getByLabel('Preserve thinking',{exact:true}).selectOption('false');
  await page.getByLabel('Eval name',{exact:true}).fill('No-thinking baseline');
  await page.getByLabel('Thinking',{exact:true}).selectOption('false');
  await page.getByLabel('Temperature',{exact:true}).fill('0');
  await page.getByLabel('Top K',{exact:true}).fill('0');
  await page.getByLabel('Budget pair').check();
  await page.screenshot({path:'/tmp/strap-new-eval.png',fullPage:true,animations:'disabled'});
  await page.getByRole('button',{name:'Run 1 task',exact:true}).click();
  await page.getByRole('heading',{name:'UI baseline',exact:true}).waitFor();
  assert.equal(await page.locator('.sidebar-link svg').count(),1);
  assert.equal(await page.locator('.side-footer').textContent().then(t=>t.includes('Settings')&&t.includes('Rescan')),true);
  assert.equal(submitted.model.generation.enable_thinking,false);
  assert.equal(submitted.model.generation.preserve_thinking,false);
  assert.equal(submitted.model.generation.temperature,0);
  assert.equal(submitted.model.generation.top_k,0);
  assert.equal(submitted.name,'No-thinking baseline');
  await page.locator('.activity-feed strong').filter({hasText:'Plan'}).waitFor();
  assert.equal(await page.locator('.activity-feed pre code').textContent(),'fmt.Println("hello")\n');
  assert.equal(await page.locator('.activity-feed img').count(),0);
  assert.equal(await page.evaluate(()=>window.pwned),undefined);
  assert.equal(await page.getByText('Only second problem',{exact:true}).count(),0);
  assert.equal(await page.locator('.feed .task').count(),0);
  assert.equal(await page.locator('.agent-tree > ul > li[data-agent="root"] > ul > li').count(),2);
  assert.equal(await page.locator('.agent-tree li[data-agent="worker"] li').count(),0);
  assert.equal(await page.locator('.activity-feed > .line').last().textContent().then(s=>s.includes('Root resumes after delegation')),true);
  assert.equal(await page.locator('.toolbar').getByRole('heading',{name:'UI baseline',exact:true}).count(),1);
  assert.equal(await page.locator('.eval-controls').getByRole('button',{name:'Cancel eval',exact:true}).count(),1);
  assert.equal(await page.getByLabel('Follow progress').count(),0);
  assert.equal(await page.getByRole('heading',{name:'Activity',exact:true}).count(),0);
  assert.equal(await page.locator('.agent-column > :first-child.eval-controls').count(),1);
  assert.equal(await page.locator('[data-metric="Model calls"] .stat-value').textContent(),'12');
  assert.equal(await page.locator('[data-metric="Tool errors"] .stat-value').textContent(),'2');
  assert.equal(await page.locator('[data-context-agent="worker"]').textContent(),'4,096 context tokens');
  assert.equal(await page.getByRole('progressbar',{name:'Overall progress',exact:true}).count(),1);
  assert.equal(await page.locator('.job-toolbar').evaluate(toolbar=>{
    const outer=toolbar.getBoundingClientRect(),picker=toolbar.querySelector('.problem-dropdown').getBoundingClientRect();
    return Math.abs((outer.left+outer.right-picker.left-picker.right)/2)<1;
  }),true);
  await page.getByLabel('Container status',{exact:true}).click();
  await page.locator('.build-overlay').getByText('Step 1: compiling image',{exact:false}).waitFor();
  await page.screenshot({path:'/tmp/strap-eval-build-overlay.png',fullPage:true,animations:'disabled'});
  buildReady=true;
  for(const response of streams)response.write(`event: progress\ndata: ${JSON.stringify({...events.find(e=>e.phase==='ready'),at:'2026-09-21T18:00:00Z'})}\n\n`);
  await page.locator('.eval-controls').getByText('Image ready',{exact:true}).waitFor();
  assert.equal(await page.locator('.agent-column > .eval-controls > .container-status').count(),1);
  assert.equal(await page.locator('.toolbar .build-indicator').count(),0);
  await page.getByLabel('Container status',{exact:true}).click();
  assert.equal(await page.locator('.job-main').evaluate(el=>el.scrollHeight<=el.clientHeight),true);
  await page.waitForFunction(()=>!document.querySelector('.problem-dropdown').open);
  await page.getByRole('button',{name:'worker',exact:true}).click();
  assert.equal(await page.getByText('Root resumes after delegation',{exact:true}).count(),0);
  await page.getByRole('button',{name:'All agents',exact:true}).click();
  await page.getByText('Root resumes after delegation',{exact:true}).waitFor();
  await page.getByRole('button',{name:'Hide sidebar',exact:true}).click();
  await page.locator('#sidebar').waitFor({state:'hidden'});
  await page.reload();
  await page.getByRole('button',{name:'Show sidebar',exact:true}).waitFor();
  await page.locator('#sidebar').waitFor({state:'hidden'});
  await page.getByRole('button',{name:'Show sidebar',exact:true}).click();
  await page.getByText('Root resumes after delegation',{exact:true}).waitFor();
  await page.screenshot({path:'/tmp/strap-eval-progress.png',fullPage:true,animations:'disabled'});
  await page.emulateMedia({colorScheme:'dark'});
  await page.screenshot({path:'/tmp/strap-eval-progress-dark.png',fullPage:true,animations:'disabled'});
  await page.locator('.problem-summary').click();
  assert.equal(await page.getByRole('progressbar',{name:'Evaluation completion'}).count(),1);
  assert.equal(await page.locator('.problem-list').evaluate(el=>el.scrollHeight>el.clientHeight&&el.clientHeight<=380),true);
  await page.screenshot({path:'/tmp/strap-eval-problem-dropdown.png',fullPage:true,animations:'disabled'});
  await page.keyboard.press('Escape');
  await page.waitForFunction(()=>!document.querySelector('.problem-dropdown').open);
  await page.locator('.problem-summary').click();
  await page.keyboard.press('Escape');
  const emitActivity=(seq,text)=>{for(const response of streams)response.write(`event: progress\ndata: ${JSON.stringify({seq,task:'easy-01',agent:'root',kind:'commentary',text,at:'2026-09-21T18:00:00Z'})}\n\n`);};
  for(let i=100;i<150;i++)emitActivity(i,`Live event ${i}`);
  await page.getByText('Live event 149',{exact:true}).waitFor();
  await page.waitForFunction(()=>{const f=document.querySelector('.activity-feed');return f.scrollHeight-f.clientHeight-f.scrollTop<5;});
  await page.locator('.activity-feed').hover();
  await page.mouse.wheel(0,-600);
  await page.waitForFunction(()=>{const f=document.querySelector('.activity-feed');return f.scrollHeight-f.clientHeight-f.scrollTop>100;});
  const readingAt=await page.locator('.activity-feed').evaluate(el=>el.scrollTop);
  emitActivity(150,'New event while reading');
  await page.getByText('New event while reading',{exact:true}).waitFor();
  assert.equal(await page.locator('.activity-feed').evaluate(el=>el.scrollTop),readingAt);
  await page.locator('.activity-feed').evaluate(async el=>{el.scrollTop=el.scrollHeight;await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));});
  emitActivity(151,'Following resumes');
  await page.getByText('Following resumes',{exact:true}).waitFor();
  await page.waitForFunction(()=>{const f=document.querySelector('.activity-feed');return f.scrollHeight-f.clientHeight-f.scrollTop<5;});
  await page.screenshot({path:'/tmp/strap-eval-auto-follow.png',fullPage:true,animations:'disabled'});
  emitActivity(152,'### Verification\n\n| Case | Result |\n| --- | --- |\n| Cycle | **Failed** |\n\n- [x] Build\n- [ ] Cycle fix\n\n> Check the count\n\n~~Done~~\n\n[unsafe](javascript:alert(1))');
  await page.locator('.activity-feed .markdown-table table').waitFor();
  assert.equal(await page.locator('.activity-feed .markdown-table td strong').textContent(),'Failed');
  assert.equal(await page.locator('.activity-feed input[type="checkbox"]').count(),2);
  assert.equal(await page.locator('.activity-feed a[href^="javascript:"]').count(),0);
  assert.equal(await page.locator('.activity-feed del').textContent(),'Done');
  assert.equal(await page.evaluate(()=>{
    const rendered=markdown('[bad](javascript:alert(1))\n\n<svg onload="window.pwned=2"></svg>\n\n| Name | Value |\n| --- | --- |\n| escaped \\| pipe | `code` |\n\n- Parent\n  - *Nested*');
    return !rendered.querySelector('svg,script,[onload],a[href^="javascript:"]') && rendered.querySelectorAll('table td').length===2 && !!rendered.querySelector('ul ul em');
  }),true);
  for(const response of streams)response.write(`event: progress\ndata: ${JSON.stringify({seq:153,task:'easy-01',agent:'worker',kind:'tool_error',text:'shell: TestCycle failed\nFull failure output',at:'2026-09-21T18:00:00Z'})}\n\n`);
  await page.locator('.attention-panel .failure-shortcut').click();
  assert.equal(await page.locator('.activity-feed .highlighted .event-tool code').textContent(),'shell');
  assert.equal(await page.locator('.activity-feed .highlighted .tool-outcome').textContent(),'Failed');
  assert.match(await page.locator('.activity-feed .highlighted .x').textContent(),/Full failure output/);
  assert.equal(await page.locator('.activity-feed details').count(),0);
  assert.equal(await page.locator('.context-panel [data-context-agent="worker"]').count(),1);
  await page.getByRole('button',{name:'All agents',exact:true}).click();
  await page.locator('.problem-summary').click();
  await page.getByRole('button').filter({hasText:'Window count'}).click();
  await page.waitForFunction(()=>!document.querySelector('.problem-dropdown').open);
  assert.equal(await page.locator('.problem-summary .problem-title').textContent(),'Window count');
  assert.equal(await page.locator('[data-metric="Model calls"] .stat-value').textContent(),'0');
  assert.equal(await page.locator('[data-metric="Elapsed"] .stat-value').textContent(),'—');
  await page.getByText('Only second problem',{exact:true}).waitFor();
  assert.equal(await page.locator('.activity-feed strong').count(),0);
  await page.setViewportSize({width:390,height:844});
  await page.screenshot({path:'/tmp/strap-eval-mobile.png',fullPage:true,animations:'disabled'});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  await page.getByRole('button',{name:'Hide sidebar',exact:true}).click();
  await page.locator('#sidebar').waitFor({state:'hidden'});
  await page.goto(`http://127.0.0.1:${server.address().port}/#/`);
  await page.locator('main').getByRole('heading',{name:'Evaluations',exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  for(const value of ['', 'true']) {
   await page.goto(`http://127.0.0.1:${server.address().port}/#/launch`);
   await page.getByLabel('Preserve thinking',{exact:true}).selectOption(value);
   await page.getByLabel('Budget pair').check();
   await page.getByRole('button',{name:'Run 1 task',exact:true}).click();
   await page.getByRole('heading',{name:'UI baseline',exact:true}).waitFor();
   assert.equal(submitted.model.generation.preserve_thinking,value===''?undefined:true);
  }
  assert.deepEqual(errors,[]);
  console.log('PASS: column filters, pagination, deletion, rename, app settings, launch request, problem fold, agent hierarchy and chronology, build feedback, sidebar persistence, safe Markdown, responsive layout');
 } finally {await browser.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1;server.close();});
