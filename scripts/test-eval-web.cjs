// Browser regression checks against a fixture API; no model or container calls.
// NODE_PATH=/path/to/node_modules node scripts/test-eval-web.cjs
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const ui = path.join(__dirname, '../internal/evalweb/ui');
const model = {backend:'vllm',base_url:'http://model:8355/v1',model:'Qwen3.6',timeout_ns:3600e9,generation:{temperature:0.6,enable_thinking:true}};
const runs = [
 {path:'old-folder-1',name:'old-folder-1',display_name:'Thinking baseline',kind:'ladder',started_at:'2026-09-21T18:00:00Z',commit:'abc1234',branch:'feature/evals',model:'Qwen3.6',configuration:model,tasks:2,passed:1,archived:false},
 {path:'old-folder-2',display_name:'Greedy comparison',kind:'ladder',started_at:'2026-09-20T18:00:00Z',commit:'def5678',branch:'main',model:'Qwen3.6',configuration:{...model,generation:{temperature:0,enable_thinking:false}},tasks:2,passed:2,archived:false},
 {path:'incomplete',display_name:'Interrupted attempt',kind:'ladder',started_at:'2026-09-19T18:00:00Z',tasks:0,passed:0,archived:true,archive_reason:'No readable final results'},
];
const tasks = [{id:'easy-01',title:'Budget pair',tier:'easy',phase:'running'},{id:'easy-02',title:'Window count',tier:'easy',phase:'queued'}];
const snap={id:'job1',name:'UI baseline',dir:'eval-123',status:'running',started_at:'2026-09-21T18:00:00Z',metadata:{model},tasks};
const events=[
 {seq:1,task:'easy-01',kind:'agent',agent:'root',text:'started'},
 {seq:2,task:'easy-01',kind:'agent',agent:'worker',parent:'root',text:'started'},
 {seq:3,task:'easy-01',kind:'commentary',agent:'worker',text:'**Plan**\n```go\nfmt.Println("hello")\n```\n<img src=x onerror="window.pwned=1">'},
 {seq:4,task:'easy-02',kind:'commentary',agent:'root',text:'Only second problem'},
];
let submitted;
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
  for(const e of events)res.write(`event: progress\ndata: ${JSON.stringify({...e,at:'2026-09-21T18:00:00Z'})}\n\n`);
  const timer=setInterval(()=>res.write(': keepalive\n\n'),1000);req.on('close',()=>clearInterval(timer));return;
 }
 if(url.pathname==='/api/library') {let body='';req.on('data',b=>body+=b);req.on('end',()=>{const edit=JSON.parse(body);const run=runs.find(r=>r.path===edit.path);if(edit.name)run.display_name=edit.name;if(edit.archived!==undefined)run.archived=edit.archived;json(edit);});return;}
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
  assert.equal(await page.getByText('Interrupted attempt',{exact:true}).count(),0);
  await page.getByLabel('Branch',{exact:true}).selectOption('@latest');
  assert.equal(await page.getByRole('link',{name:'Greedy comparison',exact:true}).count(),0);
  await page.getByRole('button',{name:'Clear filters'}).click();
  await page.getByLabel('Commit (newest run first)').selectOption('def5678');
  assert.equal(await page.getByRole('link',{name:'Thinking baseline',exact:true}).count(),0);
  await page.getByRole('button',{name:'Clear filters'}).click();
  await page.getByLabel('Thinking',{exact:true}).selectOption('false');
  assert.equal(await page.getByRole('link',{name:'Greedy comparison',exact:true}).count(),1);
  await page.getByRole('button',{name:'Clear filters'}).click();
  await page.screenshot({path:'/tmp/strap-eval-library.png',fullPage:true});
  await page.getByRole('link',{name:'Archive',exact:true}).click();
  await page.getByText('Interrupted attempt',{exact:true}).waitFor();
  await page.getByRole('button',{name:'Restore',exact:true}).click();
  await page.getByRole('link',{name:'Evaluations',exact:true}).click();
  await page.getByText('Interrupted attempt',{exact:true}).waitFor();
  page.once('dialog',dialog=>dialog.accept('Renamed experiment'));
  await page.locator('tr').filter({hasText:'Thinking baseline'}).getByRole('button',{name:'Rename'}).click();
  await page.getByRole('link',{name:'Renamed experiment',exact:true}).waitFor();
  await page.getByRole('link',{name:'New eval',exact:true}).first().click();
  await page.getByLabel('Eval name',{exact:true}).fill('No-thinking baseline');
  await page.getByLabel('Thinking',{exact:true}).selectOption('false');
  await page.getByLabel('Temperature',{exact:true}).fill('0');
  await page.getByLabel('Top K',{exact:true}).fill('0');
  await page.getByLabel('Budget pair').check();
  await page.screenshot({path:'/tmp/strap-new-eval.png',fullPage:true});
  await page.getByRole('button',{name:'Run 1 task',exact:true}).click();
  await page.getByRole('heading',{name:'UI baseline',exact:true}).waitFor();
  assert.equal(submitted.model.generation.enable_thinking,false);
  assert.equal(submitted.model.generation.temperature,0);
  assert.equal(submitted.model.generation.top_k,0);
  assert.equal(submitted.name,'No-thinking baseline');
  await page.locator('.progress-tree strong').filter({hasText:'Plan'}).waitFor();
  assert.equal(await page.locator('.progress-tree pre code').textContent(),'fmt.Println("hello")\n');
  assert.equal(await page.locator('.progress-tree img').count(),0);
  assert.equal(await page.evaluate(()=>window.pwned),undefined);
  assert.equal(await page.getByText('Only second problem',{exact:true}).count(),0);
  assert.equal(await page.locator('.feed .task').count(),0);
  assert.equal(await page.locator('.progress-tree > details > .agent-events > details').count(),1);
  await page.screenshot({path:'/tmp/strap-eval-progress.png',fullPage:true});
  await page.getByRole('row').filter({hasText:'Window count'}).click();
  await page.getByText('Only second problem',{exact:true}).waitFor();
  assert.equal(await page.locator('.progress-tree strong').count(),0);
  await page.setViewportSize({width:390,height:844});
  await page.getByRole('link',{name:'Evaluations',exact:true}).click();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  assert.deepEqual(errors,[]);
  console.log('PASS: run filters, archive/restore, rename, settings request, per-problem tree, safe Markdown, responsive layout');
 } finally {await browser.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1;server.close();});
