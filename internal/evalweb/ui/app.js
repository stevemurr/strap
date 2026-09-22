// strap eval results: one page over the /api endpoints. Hash routes:
//   #/                          welcome
//   #/run/<path>                run overview and tasks
//   #/run/<path>/task/<id>      one task with its trace
//   #/run/<path>/trial/<scenario>/<n>   one interaction trial
//   #/compare?runs=a|b|c        outcome matrix across ladder runs
'use strict';

const state = { runs: [], root: '', filter: '', selected: new Set(), open: new Set(), library: {}, sort: { key: 'task_id', dir: 1 } };
const $ = (sel, el = document) => el.querySelector(sel);
const esc = s => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const h = (tag, attrs = {}, ...kids) => {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined && v !== false) el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) if (kid !== null && kid !== undefined) el.append(kid.nodeType ? kid : document.createTextNode(kid));
  return el;
};

// mount replaces main's children, dropping null and undefined so optional
// pieces can be written inline without leaving "null" text behind.
const mount = (el, ...kids) => el.replaceChildren(...kids.flat(Infinity).filter(k => k !== null && k !== undefined));

async function api(path) {
  const res = await fetch(path);
  const body = await res.json().catch(() => ({ error: res.statusText }));
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body;
}

// Formatting. Durations arrive as nanoseconds.
const secs = ns => ns ? (ns / 1e9) : 0;
function dur(ns) {
  const s = secs(ns);
  if (s < 1) return s === 0 ? '0s' : `${(s).toFixed(1)}s`;
  if (s < 90) return `${Math.round(s)}s`;
  const m = Math.floor(s / 60), r = Math.round(s % 60);
  if (m < 60) return `${m}m${r ? String(r).padStart(2, '0') + 's' : ''}`;
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`;
}
const num = n => (n ?? 0).toLocaleString('en-US');
const pct = (a, b) => b ? `${Math.round(100 * a / b)}%` : '–';
const when = t => t && !t.startsWith('0001') ? new Date(t).toLocaleString('en-US', { dateStyle: 'medium', timeStyle: 'short' }) : '';
const kb = n => `${Math.round((n || 0) / 1024)} KB`;
const short = s => s && s.length > 8 ? s.slice(0, 7) : (s || '');

function outcomeOf(r) {
  if (!r) return 'missing';
  return r.outcome;
}
function outcomeTag(r) {
  const o = outcomeOf(r);
  const flags = [];
  if (r?.timed_out) flags.push('timed out');
  if (r?.no_reply) flags.push('no reply');
  return h('span', {}, h('span', { class: `outcome o-${o}` }, o.replace('_', ' ')), flags.length ? h('span', { class: 'flag' }, flags.join(', ')) : null);
}

// Routing
function route() {
  const hash = location.hash.slice(1) || '/';
  const [path, query = ''] = hash.split('?');
  const parts = path.split('/').filter(Boolean).map(decodeURIComponent);
  const q = new URLSearchParams(query);
  if (parts[0] === 'compare') return { view: 'compare', runs: (q.get('runs') || '').split('|').filter(Boolean) };
  if (parts[0] === 'settings') return { view: 'settings' };
  if (parts[0] === 'launch') return { view: 'launch' };
  if (parts[0] === 'job' && parts[1]) return { view: 'job', id: parts[1] };
  if (parts[0] === 'run' && parts.length >= 2) {
    const ti = parts.indexOf('task'), tr = parts.indexOf('trial');
    if (ti > 1) return { view: 'task', run: parts.slice(1, ti).join('/'), task: parts[ti + 1], tab: q.get('tab') || 'trace' };
    if (tr > 1) return { view: 'trial', run: parts.slice(1, tr).join('/'), scenario: parts[tr + 1], trial: parts[tr + 2] };
    return { view: 'run', run: parts.slice(1).join('/'), tab: q.get('tab') || 'overview' };
  }
  return { view: 'home' };
}
const runHref = p => `#/run/${p.split('/').map(encodeURIComponent).join('/')}`;
const taskHref = (p, id) => `${runHref(p)}/task/${encodeURIComponent(id)}`;
const trialHref = (p, s, n) => `${runHref(p)}/trial/${encodeURIComponent(s)}/${n}`;
const compareHref = runs => `#/compare?runs=${encodeURIComponent(runs.join('|'))}`;

// Sidebar
async function loadRuns(refresh) {
  const data = await api('/api/runs' + (refresh ? '?refresh=1' : ''));
  state.runs = data.runs || [];
  state.root = data.root;
  renderRuns();
}

function libraryRuns() {
  const batches = new Set(state.runs.filter(r => r.batch).map(r => r.path));
  return state.runs.filter(r => !batches.has(r.group));
}
function runName(r) { return r.display_name || r.name || 'Evaluation'; }
function renderRuns() {
  const settings=route().view==='settings';
  $('.sidebar-link').classList.toggle('active',!settings);
  if(route().view!=='job')$('#toolbar-title').textContent=settings?'Settings':route().view==='launch'?'New evaluation':'Evaluations';
}
async function saveRun(path, changes) {
  const response = await fetch('/api/library', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({path,...changes})});
  const result = await response.json();
  if (!response.ok) throw new Error(result.error);
  await loadRuns(true); await render();
}

function updateCompare() {
  const btn = $('#compare-btn');
  if(!btn)return;
  const n = state.selected.size;
  btn.hidden = n < 2;
  btn.textContent = `Compare ${n} runs`;
  btn.className = 'button primary';
}

// Main views
let disposeView=()=>{};
async function render() {
  disposeView();disposeView=()=>{};
  $('#toolbar-actions').replaceChildren();
  $('#toolbar-problems')?.remove();
  $('#toolbar-title').hidden=false;
  const main = $('#main');
  const r = route();
  main.classList.toggle('job-main',r.view==='job');
  $('.toolbar').classList.toggle('job-toolbar',r.view==='job');
  $('#toolbar-title').removeAttribute('title');
  if(r.view==='job')$('#toolbar-title').textContent='Loading evaluation…';
  renderRuns();
  try {
    if (r.view === 'home') { await loadRuns(false); await refreshRunning(); return renderHome(main); }
    if (r.view === 'settings') return renderSettings(main);
    if (r.view === 'launch') return await renderLaunch(main);
    if (r.view === 'job') return await renderJob(main, r.id);
    if (r.view === 'compare') return await renderCompare(main, r.runs);
    mount(main, h('div', { class: 'empty' }, 'Loading…'));
    const detail = await api(`/api/run?path=${encodeURIComponent(r.run)}`);
    if (r.view === 'run') return detail.summary.kind === 'interaction' ? renderInteraction(main, detail, r) : renderLadder(main, detail, r);
    if (r.view === 'task') return await renderTask(main, detail, r);
    if (r.view === 'trial') return await renderTrial(main, detail, r);
  } catch (err) {
    mount(main, h('div', { class: 'empty error' }, `Could not load this view: ${err.message}`));
  }
}

function preference(key, fallback) {
  try { return localStorage.getItem('eval-'+key)||fallback; } catch { return fallback; }
}
function savePreference(key,value) { try { localStorage.setItem('eval-'+key,value); } catch {} }
function applyAppearance(value) {
  if(value==='system')delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme=value;
}
function renderSettings(main) {
  const choice=(label,key,values,fallback,change)=>h('label',{class:'settings-row'},h('span',{},label),h('select',{'aria-label':label,onchange:e=>{savePreference(key,e.target.value);change?.(e.target.value);}},values.map(([v,l])=>h('option',{value:v,selected:preference(key,fallback)===v},l))));
  mount(main,h('div',{class:'head'},h('h2',{},'Settings')),h('div',{class:'app-settings'},
    h('h3',{},'Storage'),h('section',{class:'settings-card'},h('label',{class:'path-setting'},'Results path',h('input',{type:'text',readonly:true,value:state.root,'aria-label':'Results path'})),h('p',{class:'muted'},'The results folder is set when the dashboard server starts.')),
    h('h3',{},'Appearance'),h('section',{class:'settings-card'},choice('Appearance','appearance',[['system','System'],['light','Light'],['dark','Dark']],'system',applyAppearance)),
    h('h3',{},'Evaluation log'),h('section',{class:'settings-card'},choice('Default rows per page','page-size',['5','10','20','50'].map(x=>[x,x]),'10',()=>{state.library.page=1;delete state.library.pageSize;}))));
}
function renderHome(main) {
  const all = libraryRuns();
  const f = state.library;
  const body=h('tbody',{}), pager=h('div',{class:'pagination'});
  const count=h('span',{class:'muted'}), error=h('div',{class:'error',role:'status'});
  const compare=h('button',{id:'compare-btn',class:'button',hidden:true,onclick:()=>location.hash=compareHref([...state.selected])});
  const change=()=>{f.page=1;draw();};
  const search=h('input',{type:'search','aria-label':'Search evaluations',placeholder:'Search evaluations…',value:f.search||'',oninput:e=>{f.search=e.target.value;change();}});
  const unique=key=>[...new Set(all.map(r=>r[key]).filter(Boolean))];
  const branches=unique('branch'),commits=unique('commit');
  function select(key,label,values) {
    return h('select',{'aria-label':label,onchange:e=>{f[key]=e.target.value;change();}},values.map(([v,l])=>h('option',{value:v,selected:v===(f[key]||'')},l)));
  }
  const column=(title,...filters)=>h('th',{scope:'col'},h('span',{class:'column-title'},title),h('div',{class:'column-filters'},filters));
  const head=h('thead',{},h('tr',{},h('th',{'aria-label':'Compare'}),
    column('Evaluation',select('status','Status',[['','All statuses'],...unique('status').map(x=>[x,x])])),
    column('Started',h('details',{class:'date-filter'},h('summary',{},'Date range'),h('div',{class:'date-range'},...['from','to'].map(key=>h('label',{},key==='from'?'From':'To',h('input',{type:'date','aria-label':key==='from'?'Started on or after':'Started on or before',value:f[key]||'',onchange:e=>{f[key]=e.target.value;change();}})))))),
    column('Model',select('model','Model',[['','All models'],...unique('model').map(x=>[x,x])]),select('thinking','Thinking',[['','Any thinking'],['true','Thinking on'],['false','Thinking off'],['default','Server default']])),
    column('Source',select('branch','Branch',[['','All branches'],['@latest','Latest recorded branch'],['@unknown','Unknown branch'],...branches.map(x=>[x,x])]),select('commit','Commit (newest run first)',[['','All commits'],['@latest','Latest recorded commit'],...commits.map(x=>[x,short(x)])])),
    column('Passed',select('result','Result',[['','All'],['all','All passed'],['some','Has failures'],['none','No results']])),h('th',{'aria-label':'Actions'})));
  function draw() {
    const rows=all.filter(r=>{
      if(f.status&&r.status!==f.status)return false;
      if(f.search&&!JSON.stringify([runName(r),r.model,r.profile,r.commit,r.branch,r.configuration,r.role_models]).toLowerCase().includes(f.search.toLowerCase()))return false;
      const branch=f.branch==='@latest'?branches[0]:f.branch;
      if(f.branch==='@latest'&&!branches.length)return false;
      if(branch==='@unknown'?!!r.branch:(branch&&r.branch!==branch))return false;
      const commit=f.commit==='@latest'?commits[0]:f.commit;
      if(f.commit==='@latest'&&!commits.length)return false;
      if(commit&&r.commit!==commit)return false;
      if(f.model&&r.model!==f.model)return false;
      const thinking=r.configuration?.generation?.enable_thinking;
      if(f.thinking&&(f.thinking==='default'?thinking!==undefined:String(thinking)!==f.thinking))return false;
      const date=new Date(r.started_at);
      if(f.from&&(!Number.isFinite(+date)||date<new Date(f.from+'T00:00:00')))return false;
      if(f.to&&(!Number.isFinite(+date)||date>new Date(f.to+'T23:59:59.999')))return false;
      if(f.result==='all'&&(!r.tasks||r.passed!==r.tasks))return false;
      if(f.result==='some'&&!(r.tasks>r.passed))return false;
      if(f.result==='none'&&r.tasks)return false;
      return true;
    }).sort((a,b)=>f.sort==='name'?runName(a).localeCompare(runName(b)):f.sort==='score'?(b.passed/(b.tasks||1)-a.passed/(a.tasks||1)):(new Date(b.started_at)-new Date(a.started_at))*(f.sort==='old'?-1:1));
    const size=Number(f.pageSize||preference('page-size','10'));
    const pages=Math.max(1,Math.ceil(rows.length/size));
    f.page=Math.min(Math.max(1,f.page||1),pages);
    const start=(f.page-1)*size;
    count.textContent=`${rows.length} evaluations`;
    const action=(r,label,fn)=>h('button',{class:'menu-action',disabled:r.status==='running',onclick:async()=>{try{await fn();}catch(e){error.textContent=e.message;}}},label);
    mount(body,rows.slice(start,start+size).map(r=>{
      const box=h('input',{type:'checkbox','aria-label':`Compare ${runName(r)}`,disabled:r.kind!=='ladder'||!r.tasks,onchange:e=>{e.target.checked?state.selected.add(r.path):state.selected.delete(r.path);updateCompare();}});box.checked=state.selected.has(r.path);
      const date=new Date(r.started_at), valid=Number.isFinite(+date)&&date.getFullYear()>1;
      const menu=h('details',{class:'row-menu'},h('summary',{'aria-label':`Actions for ${runName(r)}`,title:'Actions'},'•••'),h('div',{class:'menu-panel'},
        action(r,'Rename',async()=>{const name=prompt('Eval name',runName(r));if(name?.trim())await saveRun(r.path,{name:name.trim()});}),
        h('button',{class:'menu-action',onclick:()=>{menu.open=false;showConfiguration(r);}},'Configuration'),
        action(r,'Delete',async()=>{if(confirm(`Permanently delete “${runName(r)}” and its files?`)){state.selected.delete(r.path);await saveRun(r.path,{delete:true});}})));
      return h('tr',{},h('td',{},box),h('td',{},r.tasks||r.job_id?h('a',{href:r.job_id?`#/job/${r.job_id}`:runHref(r.path),class:'run-title'},runName(r)):h('span',{class:'run-title'},runName(r)),h('div',{class:'run-status'},h('i',{class:r.status==='running'?'status-dot live':'status-dot'}),r.status||r.kind||'Incomplete')),
        h('td',{class:'date-cell'},valid?date.toLocaleDateString('en-US',{month:'short',day:'numeric',year:'numeric'}):'Unknown',h('div',{class:'muted'},valid?date.toLocaleTimeString('en-US',{hour:'numeric',minute:'2-digit'}):'')),
        h('td',{},r.model||'Unknown',h('div',{class:'muted'},configLabel(r.configuration))),
        h('td',{},r.branch||'Unknown branch',h('div',{class:'muted'},short(r.commit)||'Unknown commit')),
        h('td',{class:'score-cell'},h('span',{class:'score-value'},`${r.passed||0}`,h('span',{class:'muted'},` / ${r.tasks||0}`)),h('div',{class:'score-track'},h('i',{style:`width:${r.tasks?Math.min(100,100*r.passed/r.tasks):0}%`})) ),h('td',{class:'actions-cell'},menu));
    }));
    if(!rows.length)mount(body,h('tr',{},h('td',{colspan:7,class:'empty'},'No evaluations match these filters.')));
    mount(pager,h('span',{class:'muted'},rows.length?`${start+1}–${Math.min(start+size,rows.length)} of ${rows.length}`:'0 evaluations'),h('label',{},'Rows per page ',h('select',{'aria-label':'Rows per page',onchange:e=>{f.pageSize=e.target.value;change();}},['5','10','20','50'].map(n=>h('option',{value:n,selected:Number(n)===size},n)))),h('div',{class:'page-controls'},h('button',{class:'button',disabled:f.page===1,'aria-label':'Previous page',onclick:()=>{f.page--;draw();}},'‹'),h('span',{},`${f.page} / ${pages}`),h('button',{class:'button',disabled:f.page===pages,'aria-label':'Next page',onclick:()=>{f.page++;draw();}},'›')));
    updateCompare();
  }
  function showConfiguration(r) {
    const dialog=h('dialog',{class:'configuration-dialog'},h('div',{class:'head'},h('h3',{},runName(r)),h('button',{class:'button',onclick:()=>dialog.close()},'Done')),h('pre',{},JSON.stringify({model:r.configuration,roles:r.role_models,folder:r.path},null,2)));
    dialog.addEventListener('close',()=>dialog.remove());main.append(dialog);dialog.showModal();
  }
  mount(main,h('div',{class:'head library-head'},h('h2',{},'Evaluations'),count,h('a',{class:'button primary',href:'#/launch',hidden:!runner?.enabled},'New eval')),
    h('div',{class:'library-tools'},search,compare,select('sort','Sort',[['','Newest first'],['old','Oldest first'],['name','Name'],['score','Pass rate']]),h('button',{class:'button quiet',onclick:()=>{state.library={pageSize:f.pageSize};renderHome(main);}},'Clear filters')),error,
    h('div',{class:'library-card'},h('div',{class:'library-table'},h('table',{},head,body)),pager));
  draw();
}
function configLabel(model) {
 const g=model?.generation||{};
 return [g.enable_thinking===undefined?'Thinking: default':g.enable_thinking?'Thinking on':'Thinking off',g.preserve_thinking===undefined?null:`Preserve thinking ${g.preserve_thinking?'on':'off'}`,g.temperature===undefined?'Temperature: default':`Temperature ${g.temperature}`].filter(Boolean).join(' · ');
}

function header(summary, extra = []) {
  const s = summary;
  return [
    h('div', { class: 'head' }, h('h2', {}, runName(s))),
    h('div', { class: 'facts' },
      s.model ? h('span', {}, 'model ', h('b', {}, s.model)) : null,
      s.branch ? h('span', {}, 'branch ', h('b', {}, s.branch)) : null,
      s.configuration ? h('details', {}, h('summary', {}, configLabel(s.configuration)), h('pre', {}, JSON.stringify({model:s.configuration,roles:s.role_models},null,2))) : null,
      s.backend ? h('span', {}, 'backend ', h('b', {}, s.backend)) : null,
      s.profile ? h('span', {}, 'profile ', h('b', {}, s.profile)) : null,
      s.commit ? h('span', {}, 'commit ', h('code', {}, s.commit)) : null,
      s.started_at ? h('span', {}, 'started ', h('b', {}, when(s.started_at))) : null,
      s.batch ? h('span', {}, 'batch of ', h('b', {}, s.members), ' attempts, one task each') : null,
      ...extra),
  ];
}

function tabs(current, items, hrefFor) {
  return h('div', { class: 'tabs' }, items.map(([key, label]) => h('a', { class: 'tab' + (key === current ? ' active' : ''), href: hrefFor(key) }, label)));
}

// Ladder run
function renderLadder(main, detail, r) {
  const { summary, report, results } = detail;
  const graded = summary.tasks - summary.submitted;
  const kids = header(summary, [h('span', {}, h('b', {}, `${summary.passed}/${graded}`), ` graded tasks passed (${pct(summary.passed, graded)})`), summary.submitted ? h('span', {}, h('b', {}, summary.submitted), ' awaiting grading') : null]);
  kids.push(tabs(r.tab, [['overview', 'Overview'], ['tasks', 'Tasks'], ['failures', 'Failures'], ['tools', 'Tools']], k => `${runHref(r.run)}?tab=${k}`));
  if (r.tab === 'overview') kids.push(tierTable(report.tiers), h('div', { class: 'section' }, h('h3', {}, 'Outcomes'), outcomeStrip(report.tasks)));
  if (r.tab === 'tasks') kids.push(taskTable(r.run, report.tasks, results));
  if (r.tab === 'failures') kids.push(failures(r.run, report.tasks, results));
  if (r.tab === 'tools') kids.push(toolTable(report.tasks));
  mount(main, kids);
}

function tierTable(tiers) {
  const cols = [['tier', 'tier'], ['tasks', 'tasks'], ['passed', 'passed'], ['rate', 'rate'], ['failed', 'failed'], ['build failed', 'build_failed'], ['error', 'errored'], ['timed out', 'timed_out'], ['no reply', 'no_reply'], ['submitted', 'submitted'], ['mean time', 'mean_duration_ns'], ['calls', 'mean_model_calls'], ['tools', 'mean_tool_calls'], ['tool errs', 'mean_tool_errors'], ['agents', 'mean_agents'], ['tokens in', 'mean_input_tokens'], ['tokens out', 'mean_output_tokens']];
  const rows = (tiers || []).map(t => h('tr', {}, cols.map(([, k]) => {
    if (k === 'tier') return h('td', { class: 'id' }, t.tier);
    if (k === 'rate') return h('td', { class: 'num' }, pct(t.passed, t.tasks - t.submitted));
    if (k === 'mean_duration_ns') return h('td', { class: 'num' }, dur(t[k]));
    const v = t[k];
    return h('td', { class: 'num' }, Number.isInteger(v) ? num(v) : k.includes('tokens') ? num(Math.round(v || 0)) : (v || 0).toFixed(1));
  })));
  return h('div', { class: 'section' }, h('h3', {}, 'Tiers'), h('table', { class: 'dense' }, h('thead', {}, h('tr', {}, cols.map(([label, k]) => h('th', { class: k === 'tier' ? '' : 'num' }, label)))), h('tbody', {}, rows)));
}

function outcomeStrip(tasks) {
  const counts = {};
  for (const t of tasks) counts[t.outcome] = (counts[t.outcome] || 0) + 1;
  return h('div', { class: 'legend' }, Object.entries(counts).sort((a, b) => b[1] - a[1]).map(([o, n]) => h('span', {}, h('i', { class: `cell ${o}`, style: `background: var(--${o === 'build_failed' ? 'build' : o})` }), `${o.replace('_', ' ')} ${n}`)));
}

const taskCols = [
  ['task', 'task_id', 'id'], ['outcome', 'outcome', ''], ['time', 'duration_ns', 'num'], ['first reply', 'time_to_first_reply_ns', 'num'], ['calls', 'model_calls', 'num'],
  ['longest call', 'longest_call_ns', 'num'], ['reasoning', 'reasoning_bytes', 'num'], ['failed outputs', 'output_failures', 'num'], ['ctx max', 'max_context_tokens', 'num'],
  ['tools', 'tool_total', 'num'], ['tool errs', 'tool_error_total', 'num'], ['agents', 'agents', 'num'], ['work', 'work_total', 'num'], ['replies', 'replies', 'num'], ['tokens in', 'input_tokens', 'num'], ['tokens out', 'output_tokens', 'num'],
];
function cellText(t, k) {
  if (k.endsWith('_ns')) return dur(t[k]);
  if (k === 'reasoning_bytes') return kb(t[k]);
  if (k === 'work_total') return num(Object.values(t.work || {}).reduce((a, b) => a + b, 0));
  return num(t[k]);
}
function sortTasks(tasks) {
  const { key, dir } = state.sort;
  const val = t => key === 'work_total' ? Object.values(t.work || {}).reduce((a, b) => a + b, 0) : t[key];
  return [...tasks].sort((a, b) => {
    const x = val(a), y = val(b);
    if (x === y) return a.task_id.localeCompare(b.task_id);
    return (x > y ? 1 : -1) * dir;
  });
}
function taskTable(run, tasks, results) {
  const table = h('table', { class: 'dense' });
  const draw = () => {
    table.replaceChildren(
      h('thead', {}, h('tr', {}, taskCols.map(([label, k, cls]) => h('th', { class: `${cls} sortable${state.sort.key === k ? ' sorted' : ''}`, onclick: () => { state.sort = { key: k, dir: state.sort.key === k ? -state.sort.dir : (k === 'task_id' ? 1 : -1) }; draw(); } }, label, state.sort.key === k ? (state.sort.dir > 0 ? ' ▲' : ' ▼') : '')))),
      h('tbody', {}, sortTasks(tasks).map(t => h('tr', { class: 'row', onclick: () => location.hash = taskHref(run, t.task_id) },
        taskCols.map(([, k, cls]) => k === 'task_id' ? h('td', { class: 'id', title: t.title }, t.task_id) : k === 'outcome' ? h('td', {}, outcomeTag(results[t.task_id] || t)) : h('td', { class: cls }, cellText(t, k)))))));
  };
  draw();
  return h('div', { class: 'section scroll' }, table);
}

function failures(run, tasks, results) {
  const failed = tasks.filter(t => (t.outcome !== 'submitted' && !t.passed) || t.error);
  if (!failed.length) return h('div', { class: 'empty' }, 'Every graded task passed.');
  return h('div', { class: 'section' }, failed.map(t => {
    const r = results[t.task_id] || {};
    const notes = [t.execution_error && `execution: ${t.execution_error}`, t.capture_error && `capture: ${t.capture_error}`, t.error && `analysis: ${t.error}`].filter(Boolean);
    return h('div', { class: 'section' },
      h('div', {}, h('a', { class: 'mono', href: taskHref(run, t.task_id) }, t.task_id), ' ', outcomeTag(r), h('span', { class: 'flag' }, t.tier)),
      notes.length ? h('div', { class: 'flag' }, notes.join(' · ')) : null,
      t.grade_tail ? h('pre', { class: 'grade', style: 'margin-top:6px' }, t.grade_tail) : null);
  }));
}

function toolTable(tasks) {
  const calls = {}, errors = {};
  for (const t of tasks) {
    for (const [k, v] of Object.entries(t.tool_calls || {})) calls[k] = (calls[k] || 0) + v;
    for (const [k, v] of Object.entries(t.tool_errors || {})) errors[k] = (errors[k] || 0) + v;
  }
  const names = Object.keys(calls).sort((a, b) => calls[b] - calls[a]);
  if (!names.length) return h('div', { class: 'empty' }, 'No tool calls recorded.');
  return h('div', { class: 'section' }, h('table', { class: 'dense', style: 'width:auto;min-width:360px' }, h('thead', {}, h('tr', {}, h('th', {}, 'tool'), h('th', { class: 'num' }, 'calls'), h('th', { class: 'num' }, 'errors'), h('th', { class: 'num' }, 'error rate'))),
    h('tbody', {}, names.map(n => h('tr', {}, h('td', { class: 'id' }, n), h('td', { class: 'num' }, num(calls[n])), h('td', { class: 'num' }, num(errors[n] || 0)), h('td', { class: 'num' }, pct(errors[n] || 0, calls[n])))))));
}

// One ladder task
async function renderTask(main, detail, r) {
  const t = detail.report.tasks.find(x => x.task_id === r.task);
  const res = detail.results[r.task];
  if (!t || !res) { mount(main, h('div', { class: 'empty' }, `No task ${r.task} in this run.`)); return; }
  const facts = [['outcome', outcomeTag(res)], ['tier', t.tier], ['time', dur(t.duration_ns)], ['first reply', dur(t.time_to_first_reply_ns)], ['model calls', num(t.model_calls)], ['longest call', dur(t.longest_call_ns)],
    ['tokens in / out', `${num(t.input_tokens)} / ${num(t.output_tokens)}`], ['context max', num(t.max_context_tokens)], ['reasoning', kb(t.reasoning_bytes)], ['tools', `${num(t.tool_total)} (${num(t.tool_error_total)} errors)`],
    ['agents', `${t.agents} ${Object.entries(t.roles || {}).map(([k, v]) => `${k} ${v}`).join(', ')}`], ['work events', Object.entries(t.work || {}).map(([k, v]) => `${k} ${v}`).join(', ') || '0'], ['audits', Object.entries(t.audits || {}).map(([k, v]) => `${k} ${v}`).join(', ') || 'none'],
    ['replies', num(t.replies)], ['session', res.session || ''], ['grade', res.grade ? `${res.grade.passed ? 'passed' : 'failed'} · compiled ${res.grade.compiled} · ${dur(res.grade.duration_ns)}` : 'not graded']];
  const kids = [
    h('div', { class: 'head' }, h('h2', {}, r.task), h('a', { href: runHref(r.run) + '?tab=tasks' }, '← all tasks')),
    h('div', { class: 'facts' }, h('span', {}, h('b', {}, t.title))),
    h('dl', { class: 'kv section' }, facts.map(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
    tabs(r.tab, [['trace', 'Trace'], ['grade', 'Grade output'], ['reply', 'Final reply']], k => `${taskHref(r.run, r.task)}?tab=${k}`),
  ];
  if (r.tab === 'grade') kids.push(res.grade ? h('pre', { class: 'grade' }, res.grade.command + '\n\n' + (res.grade.output || '(no output)')) : h('div', { class: 'empty' }, 'This task was not graded.'));
  else if (r.tab === 'reply') kids.push(res.reply ? h('pre', { class: 'grade' }, res.reply) : h('div', { class: 'empty' }, 'No reply was recorded.'), res.error ? h('div', { class: 'error' }, res.error) : null);
  else kids.push(await traceView(r.run, `${r.task}/trace.jsonl`));
  mount(main, kids);
}

// Interaction run
function renderInteraction(main, detail, r) {
  const { summary, report } = detail;
  const t = summary.trials || {};
  const kids = header(summary, [h('span', {}, 'mode ', h('b', {}, t.mode)), h('span', {}, h('b', {}, `${t.passed}/${t.completed}`), ' trials passed'), t.planned ? h('span', {}, h('b', {}, t.planned), ' planned') : null, h('span', {}, h('b', {}, t.clean), ' clean, ', h('b', {}, t.recovered), ' recovered, ', h('b', {}, t.scorable), ' scorable')]);
  const rows = report.results.map(x => h('tr', { class: 'row', onclick: () => location.hash = trialHref(r.run, x.scenario_id, x.trial) },
    h('td', { class: 'id' }, x.scenario_id), h('td', { class: 'num' }, x.trial), h('td', {}, h('span', { class: `outcome o-${x.outcome}` }, x.outcome), x.error_class ? h('span', { class: 'flag' }, x.error_class) : null),
    h('td', { class: 'num' }, `${x.harness.passed}/${x.harness.total}`), h('td', {}, flags(x.behavior)), h('td', { class: 'id' }, x.stop_reason), h('td', { class: 'num' }, x.model_calls), h('td', { class: 'num' }, x.tool_calls),
    h('td', { class: 'num' }, x.input_tokens == null ? '–' : num(x.input_tokens)), h('td', { class: 'num' }, x.output_tokens == null ? '–' : num(x.output_tokens)), h('td', { class: 'num' }, dur(x.duration_ns))));
  kids.push(h('div', { class: 'section scroll' }, h('table', { class: 'dense' }, h('thead', {}, h('tr', {}, ['scenario', 'trial', 'outcome', 'harness', 'behavior', 'stopped by', 'calls', 'tools', 'tokens in', 'tokens out', 'time'].map((l, i) => h('th', { class: i > 5 || i === 1 || i === 3 ? 'num' : '' }, l)))), h('tbody', {}, rows))));
  mount(main, kids);
}
function flags(b) {
  const on = [b.clean_success && 'clean', b.recovery_success && 'recovered', b.outcome_correct && 'correct', !b.scorable && 'unscorable'].filter(Boolean);
  const bad = [b.rejected_calls && `${b.rejected_calls} rejected`, b.unknown_tool_errors && `${b.unknown_tool_errors} unknown tools`, b.output_errors && `${b.output_errors} output errors`].filter(Boolean);
  return h('span', {}, on.join(', '), bad.length ? h('span', { class: 'flag error' }, bad.join(', ')) : null);
}

async function renderTrial(main, detail, r) {
  const x = detail.report.results.find(y => y.scenario_id === r.scenario && String(y.trial) === String(r.trial));
  if (!x) { mount(main, h('div', { class: 'empty' }, 'No such trial.')); return; }
  const kids = [
    h('div', { class: 'head' }, h('h2', {}, `${x.scenario_id} · trial ${x.trial}`), h('a', { href: runHref(r.run) }, '← all trials')),
    h('div', { class: 'facts' }, h('span', {}, h('span', { class: `outcome o-${x.outcome}` }, x.outcome)), x.error ? h('span', { class: 'error' }, x.error) : null, h('span', {}, 'stopped by ', h('b', {}, x.stop_reason)), h('span', {}, 'harness ', h('b', {}, `${x.harness.passed}/${x.harness.total}`)), h('span', {}, flags(x.behavior))),
    h('div', { class: 'section' }, h('h3', {}, 'Assertions'), h('table', { class: 'dense' }, h('thead', {}, h('tr', {}, h('th', {}, 'track'), h('th', {}, 'assertion'), h('th', {}, 'result'), h('th', {}, 'expected'), h('th', {}, 'actual'))),
      h('tbody', {}, (x.assertions || []).map(a => h('tr', {}, h('td', { class: 'id' }, a.track), h('td', { class: 'id' }, a.id), h('td', {}, h('span', { class: `outcome o-${a.passed ? 'passed' : 'failed'}` }, a.passed ? 'passed' : 'failed')), h('td', {}, h('code', {}, JSON.stringify(a.expected))), h('td', {}, h('code', {}, JSON.stringify(a.actual)))))))),
    x.schema ? h('div', { class: 'facts' }, h('span', {}, 'schema attempts ', h('b', {}, x.schema.attempts)), h('span', {}, 'invalid calls ', h('b', {}, x.schema.invalid_calls)), h('span', {}, 'first tool correct ', h('b', {}, String(x.schema.first_tool_correct))), h('span', {}, 'first arguments valid ', h('b', {}, String(x.schema.first_arguments_valid)))) : null,
    h('h3', { style: 'margin-bottom:8px' }, 'Trace'),
    await traceView(r.run, x.trace),
  ];
  mount(main, kids);
}

// Trace viewer: kind chips, agent select, load more.
async function traceView(run, file) {
  const box = h('div', { class: 'section' });
  const filter = { kinds: new Set(), agent: '', after: 0 };
  const list = h('div', { class: 'events' });
  const bar = h('div', { class: 'filters' });
  const more = h('div', { class: 'more' });
  let page;
  async function load(reset) {
    if (reset) { filter.after = 0; list.replaceChildren(); }
    const q = new URLSearchParams({ run, file, after: filter.after, limit: 200 });
    if (filter.kinds.size) q.set('kinds', [...filter.kinds].join(','));
    if (filter.agent) q.set('agent', filter.agent);
    page = await api(`/api/trace?${q}`);
    for (const e of page.events) list.append(eventRow(e));
    if (!page.events.length && !list.children.length) list.append(h('div', { class: 'empty', style: 'grid-column:1/-1;padding:16px' }, 'No events match these filters.'));
    more.replaceChildren(page.next ? h('button', { class: 'button', onclick: () => { filter.after = page.next; load(false); } }, 'Load more') : h('span', { class: 'flag' }, `${list.querySelectorAll('.ev').length} of ${page.total} events shown`));
    if (reset) drawBar();
  }
  function drawBar() {
    mount(bar,
      ...Object.entries(page.kinds).sort((a, b) => b[1] - a[1]).map(([k, n]) => h('button', { class: 'chip' + (filter.kinds.has(k) ? ' on' : ''), onclick: () => { filter.kinds.has(k) ? filter.kinds.delete(k) : filter.kinds.add(k); load(true); } }, k, h('span', {}, n))),
      page.agents.length > 1 ? h('select', { onchange: e => { filter.agent = e.target.value; load(true); } }, h('option', { value: '' }, 'all agents'), page.agents.map(a => h('option', { value: a, selected: a === filter.agent || null }, a))) : null,
      h('span', { class: 'flag' }, filter.kinds.size ? '' : 'output_delta hidden until selected'),
      h('span', { class: 'flag' }, page.first ? `${dur((new Date(page.last) - new Date(page.first)) * 1e6)} span` : ''));
  }
  box.append(bar, list, more);
  try { await load(true); } catch (err) { box.replaceChildren(h('div', { class: 'error' }, `Trace unavailable: ${err.message}`)); }
  return box;
}

function eventRow(e) {
  const p = e.payload || {};
  const cls = ['ev', e.kind, (p.error || p.status === 'failed' || p.rejected_tool_call) ? 'error' : ''].join(' ');
  return h('div', { class: cls }, h('div', { class: 'seq' }, e.sequence), h('div', { class: 't' }, new Date(e.time).toLocaleTimeString('en-US', { hour12: false }) + '.' + String(new Date(e.time).getMilliseconds()).padStart(3, '0')), h('div', { class: 'k' }, e.kind, e.agent ? h('div', { class: 'flag', style: 'margin:0' }, e.agent) : null), h('div', { class: 'body' }, summary(e.kind, p)));
}

// Known kinds render a one-line summary with the payload folded beneath.
function summary(kind, p) {
  const raw = h('details', {}, h('summary', {}, 'payload'), h('pre', {}, JSON.stringify(p, null, 2)));
  const line = (...parts) => h('div', {}, ...parts, raw);
  switch (kind) {
    case 'tool': {
      const args = p.arguments ? (typeof p.arguments === 'string' ? p.arguments : JSON.stringify(p.arguments)) : '';
      const status = p.finished_at ? (p.error ? h('span', { class: 'err' }, ` error: ${p.error.message || p.error}`) : ' done') : ' started';
      return line(h('b', {}, p.name || p.call?.name || ''), status, h('pre', {}, args.slice(0, 600)), p.result?.content ? h('pre', {}, textOf(p.result.content).slice(0, 600)) : null);
    }
    case 'usage': { const u = p.observation?.usage || p.usage || {}; return line(`in ${num(u.input_tokens)} · out ${num(u.output_tokens)}`); }
    case 'context_tokens': return line(`context ${num(p.count ?? p.tokens)} tokens`);
    case 'output_finished': return line(p.status || 'finished', p.reasoning_bytes ? ` · reasoning ${kb(p.reasoning_bytes)}` : '', p.error ? h('span', { class: 'err' }, ` · ${p.error.message || JSON.stringify(p.error)}`) : '');
    case 'message': case 'reply': case 'instruction': case 'notification': case 'commentary': {
      const m = p.message || p;
      return line(m.from ? h('span', { class: 'flag', style: 'margin:0 6px 0 0' }, `${m.from} → ${m.to}`) : null, markdown(String(m.content ?? p.content ?? '')));
    }
    case 'agent_state': return line(`${p.agent?.agent_id || ''} ${p.agent?.state || p.state || ''}`);
    case 'agent_started': return line(`${p.agent?.agent_id || ''} started with ${(p.tools || []).length} tools`);
    case 'work': case 'implementation': case 'audit': case 'plan': case 'research': return line(p.event || p.kind || '', h('pre', {}, JSON.stringify(p.change || p.work || p, null, 0).slice(0, 400)));
    default: return line(h('pre', {}, JSON.stringify(p).slice(0, 300)));
  }
}
function textOf(content) {
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) return content.map(c => c.text || '').join('');
  return JSON.stringify(content);
}

// Compare
async function renderCompare(main, paths) {
  if (paths.length < 2) { mount(main, h('div', { class: 'empty' }, 'Select at least two evaluations in the library, then press Compare.')); return; }
  mount(main, h('div', { class: 'empty' }, `Analysing ${paths.length} runs…`));
  const data = await api('/api/compare?' + paths.map(p => `run=${encodeURIComponent(p)}`).join('&'));
  const runs = data.runs;
  const tasks = new Map();
  for (const r of runs) for (const t of r.report.tasks) if (!tasks.has(t.task_id)) tasks.set(t.task_id, t.tier);
  const tiers = ['easy', 'medium', 'hard'];
  const ordered = [...tasks].sort((a, b) => (tiers.indexOf(a[1]) - tiers.indexOf(b[1])) || a[0].localeCompare(b[0]));
  const at = (r, id) => r.report.tasks.find(t => t.task_id === id);
  const label = r => runName(r.summary);
  const rows = [];
  let lastTier = '';
  for (const [id, tier] of ordered) {
    if (tier !== lastTier) { rows.push(h('tr', { class: 'tier' }, h('td', { class: 'id' }, tier), runs.map(() => h('td')))); lastTier = tier; }
    rows.push(h('tr', {}, h('td', { class: 'id' }, id), runs.map(r => {
      const t = at(r, id);
      const res = r.results[id];
      const title = t ? `${label(r)}\n${id}: ${t.outcome}${res?.timed_out ? ' (timed out)' : ''} · ${dur(t.duration_ns)} · ${t.model_calls} calls · ${num(t.input_tokens)} in / ${num(t.output_tokens)} out` : `${label(r)}\n${id}: not run`;
      return h('td', {}, h('a', { class: `cell ${t ? t.outcome : ''}${res?.timed_out ? ' timed_out' : ''}`, href: t ? taskHref(r.summary.path, id) : null, title }));
    })));
  }
  const graded = r => r.summary.tasks - r.summary.submitted;
  const base = runs[0];
  const metric = (name, get, fmt, betterLow) => h('tr', {}, h('td', { class: 'id' }, name), runs.map((r, i) => {
    const v = get(r), b = get(base);
    const d = i === 0 || b == null || v == null ? null : v - b;
    const cls = d === null || d === 0 ? '' : ((d < 0) === !!betterLow ? 'up' : 'down');
    return h('td', { class: 'num', style: 'padding:4px 6px' }, fmt(v), d !== null && d !== 0 ? h('div', { class: `delta ${cls}` }, (d > 0 ? '+' : '') + fmt(d)) : null);
  }));
  const mean = (r, k) => r.report.tasks.length ? r.report.tasks.reduce((a, t) => a + (t[k] || 0), 0) / r.report.tasks.length : 0;
  mount(main, 
    h('div', { class: 'head' }, h('h2', {}, `Compare ${runs.length} runs`)),
    h('div', { class: 'legend' }, ['passed', 'failed', 'build_failed', 'error', 'submitted'].map(o => h('span', {}, h('i', { class: `cell ${o}`, style: `background:var(--${o === 'build_failed' ? 'build' : o})` }), o.replace('_', ' '))), h('span', {}, h('i', { class: 'cell timed_out', style: 'background:var(--failed)' }), 'timed out'), h('span', {}, h('i', { style: 'background:var(--rule)' }), 'not run'), h('span', {}, 'deltas are against the first run')),
    h('div', { class: 'section', style: 'overflow:auto' }, h('table', { class: 'matrix' },
      h('thead', {}, h('tr', {}, h('th'), runs.map(r => h('th', { class: 'col', title: r.summary.path }, label(r))))),
      h('tbody', {}, rows,
        h('tr', { class: 'tier' }, h('td', { class: 'id' }, 'passed'), runs.map(r => h('td', { class: 'rate' }, `${r.summary.passed}/${graded(r)}`))),
        h('tr', {}, h('td', { class: 'id' }, 'rate'), runs.map(r => h('td', { class: 'rate' }, pct(r.summary.passed, graded(r)))))))),
    h('div', { class: 'section' }, h('h3', {}, 'Run metrics'), h('table', { class: 'dense', style: 'width:auto' },
      h('thead', {}, h('tr', {}, h('th'), runs.map(r => h('th', { class: 'num', title: r.summary.path }, label(r))))),
      h('tbody', {},
        metric('model', r => r.summary.model, v => v || '', false),
        metric('profile', r => r.summary.profile, v => v || '', false),
        metric('commit', r => r.summary.commit, v => short(v), false),
        metric('tasks', r => r.summary.tasks, num, false),
        metric('passed', r => r.summary.passed, num, false),
        metric('pass rate', r => graded(r) ? r.summary.passed / graded(r) : null, v => v == null ? '–' : `${Math.round(100 * v)}%`, false),
        metric('timed out', r => r.summary.timed_out, num, true),
        metric('mean time', r => mean(r, 'duration_ns'), dur, true),
        metric('mean calls', r => mean(r, 'model_calls'), v => (v || 0).toFixed(1), true),
        metric('mean tools', r => mean(r, 'tool_total'), v => (v || 0).toFixed(1), true),
        metric('mean tool errors', r => mean(r, 'tool_error_total'), v => (v || 0).toFixed(2), true),
        metric('mean tokens in', r => mean(r, 'input_tokens'), v => num(Math.round(v || 0)), true),
        metric('mean tokens out', r => mean(r, 'output_tokens'), v => num(Math.round(v || 0)), true),
        metric('mean reasoning', r => mean(r, 'reasoning_bytes'), kb, true)))));
}

// Launching container runs with a configuration snapshot per evaluation.
let runner = null;
async function loadRunner() {
  try { runner = await api('/api/runner'); } catch { runner = { enabled: false }; }
  await refreshRunning();
}
async function refreshRunning() {
  const el = $('#running');
  try {
    const { jobs } = await api('/api/jobs');
    const live = jobs.find(j => j.status === 'running');
    el.hidden = !live;
    if (live) {
      const done = live.tasks.filter(t => t.phase === 'finished').length;
      el.href = `#/job/${live.id}`;
      el.replaceChildren(h('span', { class: 'pulse' }), `Running ${done}/${live.tasks.length} · `, h('b', {}, live.name));
    }
  } catch { el.hidden = true; }
}

async function renderLaunch(main) {
  if (!runner?.enabled) { mount(main, h('div', { class: 'empty' }, 'Running is disabled. Start strap eval web with a ladder directory and model flags to run tasks from here.')); return; }
  const chosen = new Set();
  const form=h('form',{class:'eval-form',onsubmit:e=>e.preventDefault()});
  const name=h('input',{type:'text',maxlength:160,placeholder:'e.g. Qwen · no thinking · baseline','aria-label':'Eval name'});
  const defaults=runner.configuration || {model:runner.model,backend:runner.backend,base_url:runner.base_url,timeout_ns:3600e9,generation:{}};
  const input=(value,attrs={})=>h('input',{value:value??'',...attrs});
  const model=input(defaults.model,{required:true,list:'known-models'});
  const endpoint=input(defaults.base_url,{type:'url',required:true});
  const backend=h('select',{},['vllm','chatcompletions'].map(x=>h('option',{value:x,selected:x===defaults.backend},x)));
  const timeout=input(defaults.timeout_ns/60e9,{type:'number',min:0.01,step:'any',required:true});
  const field=(label,control)=>{if(!control.hasAttribute('aria-label'))control.setAttribute('aria-label',label);return h('label',{class:'field'},label,control);};
  const generation=h('fieldset',{class:'settings-grid'});
  const settings={};
  const g=defaults.generation||{};
  const choice=(key,label,values)=>{
    const value=g[key]===undefined?'':String(g[key]);
    settings[key]=h('select',{},[['','Server default'],...values].map(([v,l])=>h('option',{value:v,selected:v===value},l)));
    generation.append(field(label,settings[key]));
  };
  choice('enable_thinking','Thinking',[['true','Enabled'],['false','Disabled']]);
  choice('preserve_thinking','Preserve thinking',[['true','Enabled'],['false','Disabled']]);
  settings.preserve_thinking.parentElement.append(h('span',{class:'muted'},'Keep thinking from earlier turns when supported by the model.'));
  choice('reasoning_effort','Reasoning effort',[['low','Low'],['medium','Medium'],['xhigh','Extra high']]);
  choice('force_nonempty_content','Require nonempty content',[['true','Enabled'],['false','Disabled']]);
  for(const [key,label,min,max,step] of [
    ['temperature','Temperature',0,2,'any'],['top_p','Top P',0.000001,1,'any'],['top_k','Top K',-1,null,1],['min_p','Min P',0,1,'any'],
    ['presence_penalty','Presence penalty',-2,2,'any'],['repetition_penalty','Repetition penalty',0.000001,null,'any'],['max_tokens','Maximum output tokens',1,null,1]]) {
    settings[key]=input(g[key],{type:'number',min,max,step,placeholder:'Server default'});generation.append(field(label,settings[key]));
  }
  const allRoles=h('input',{type:'checkbox',checked:!Object.values(runner.roles||{}).some(Boolean)});
  const syncBackend=()=>generation.disabled=backend.value!=='vllm';backend.addEventListener('change',syncBackend);syncBackend();
  form.append(h('div',{class:'settings-grid'},field('Eval name (optional)',name),field('Model',model),field('Backend',backend),field('Model endpoint',endpoint),field('Request timeout (minutes)',timeout)),
    h('datalist',{id:'known-models'},[...new Set([defaults.model,...state.runs.map(r=>r.model)].filter(Boolean))].map(x=>h('option',{value:x}))),
    h('h3',{},'Generation settings'),h('p',{class:'muted'},'Blank fields use server defaults. Thinking options require support in the served model template. The endpoint must be reachable from the eval container.'),generation,
    h('label',{},allRoles,' Apply this model and generation settings to every agent'),
    h('details',{},h('summary',{},'Existing role overrides (retained when unchecked)'),h('pre',{},JSON.stringify(runner.roles||{},null,2))));
  const tiers = new Map();
  for (const t of runner.tasks || []) { if (!tiers.has(t.tier)) tiers.set(t.tier, []); tiers.get(t.tier).push(t); }
  const start = h('button', { class: 'button primary', disabled: true }, 'Run 0 tasks');
  const note = h('span', { class: 'flag' });
  const boxes = new Map();
  const sync = () => { start.disabled = chosen.size === 0; start.textContent = `Run ${chosen.size} task${chosen.size === 1 ? '' : 's'}`; for (const [id, box] of boxes) box.checked = chosen.has(id); };
  const picker = h('div', { class: 'picker' }, [...tiers].map(([tier, tasks]) => {
    const all = h('button', { class: 'button small', onclick: () => { const every = tasks.every(t => chosen.has(t.id)); for (const t of tasks) every ? chosen.delete(t.id) : chosen.add(t.id); sync(); } }, 'all');
    return h('div', { class: 'tier' }, h('div', { class: 'tier-head' }, h('b', {}, `${tier} · ${tasks.length}`), all), tasks.map(t => {
      const box = h('input', { type: 'checkbox', onchange: e => { e.target.checked ? chosen.add(t.id) : chosen.delete(t.id); sync(); } });
      boxes.set(t.id, box);
      return h('label', {}, box, h('span', {}, h('span', { class: 'mono' }, t.id), h('div', { class: 'title' }, t.title)));
    }));
  }));
  start.addEventListener('click', async () => {
    if (!form.reportValidity()) return;
    start.disabled = true;
    note.textContent = 'Starting…';
    try {
      const res = await fetch('/api/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ tasks: [...chosen], name:name.value.trim(), apply_to_roles:allRoles.checked, model:{model:model.value.trim(),backend:backend.value,base_url:endpoint.value.trim(),timeout_ns:Math.round(Number(timeout.value)*60e9),generation:backend.value==='vllm'?Object.fromEntries(Object.entries(settings).filter(([,el])=>el.value!=='').map(([key,el])=>[key,['enable_thinking','preserve_thinking','force_nonempty_content'].includes(key)?el.value==='true':key==='reasoning_effort'?el.value:Number(el.value)])):{}} }) });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error || res.statusText);
      location.hash = `#/job/${body.id}`;
    } catch (err) { note.textContent = err.message; start.disabled = false; }
  });
  mount(main, 
    h('div', { class: 'head' }, h('h2', {}, 'New eval'), h('a',{href:'#/'},'← Evaluations')),
    form,
    runner.error ? h('div', { class: 'error' }, runner.error) : null,
    h('div', { class: 'empty', style: 'padding:0 0 12px' }, 'Choose problems below. Each runs in a fresh container at /workspace and is graded in a separate container. Settings are saved with this evaluation.'),
    h('div', { class: 'launch-bar' }, start, note),
    picker,
  );
}

async function renderJob(main, id) {
  let snap = await api(`/api/jobs/${id}`);
  let selected=snap.tasks.find(t=>['running','starting','grading'].includes(t.phase))?.id || snap.tasks[0]?.id;
  const events=[];const seen=new Set();
  let agentFilter='', pinnedProblem=false;
  const table=h('div',{class:'problem-list','aria-label':'Problems'});
  const problemRows=new Map();
  const problemSummary=h('summary',{class:'problem-summary','aria-label':'Choose problem and view evaluation progress'});
  const overview=h('div',{class:'problem-overview'});
  const problems=h('details',{id:'toolbar-problems',class:'problem-dropdown'},problemSummary,h('div',{class:'problem-popover'},overview,table));
  let foldAnimation;
  problems.setExpanded=open=>{
    foldAnimation?.cancel();
    const panel=problems.querySelector('.problem-popover');
    problemSummary.setAttribute('aria-expanded',String(open));
    panel.inert=!open;
    if(open)problems.open=true;
    if(matchMedia('(prefers-reduced-motion: reduce)').matches){problems.open=open;return;}
    foldAnimation=panel.animate(open?[{opacity:0,transform:'translateY(-5px) scale(.985)'},{opacity:1,transform:'none'}]:[{opacity:1,transform:'none'},{opacity:0,transform:'translateY(-5px) scale(.985)'}],{duration:170,easing:'ease',fill:'both'});
    foldAnimation.onfinish=()=>{problems.open=open;foldAnimation.cancel();foldAnimation=null;};
  };
  problemSummary.setAttribute('aria-expanded','false');
  problemSummary.addEventListener('click',e=>{e.preventDefault();problems.setExpanded(problemSummary.getAttribute('aria-expanded')!=='true');});
  const feed=h('div',{class:'feed activity-feed'});
  const tree=h('nav',{class:'agent-tree','aria-label':'Agent hierarchy'});
  const contextPanel=h('section',{class:'context-panel','aria-label':'Agent context usage'});
  const attention=h('section',{class:'attention-panel','aria-label':'Needs attention',hidden:true});
  const stats=h('div',{class:'problem-stats','aria-label':'Problem statistics'});
  const controlIcon=path=>{
    const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');
    for(const [key,value] of Object.entries({width:18,height:18,viewBox:'0 0 24 24',fill:'none',stroke:'currentColor','stroke-width':1.6,'stroke-linecap':'round','stroke-linejoin':'round','aria-hidden':'true'}))svg.setAttribute(key,value);
    const shape=document.createElementNS(svg.namespaceURI,'path');shape.setAttribute('d',path);svg.append(shape);return svg;
  };
  const buildLabel=h('span',{},'Waiting');
  const buildOutput=h('pre',{});
  const buildPanel=h('div',{class:'build-overlay',id:'container-log-panel',hidden:true},h('div',{class:'container-log-heading'},h('h3',{},'Container'),buildLabel),buildOutput);
  const build=h('div',{class:'build-indicator container-status'},h('button',{class:'toolbar-button container-toggle','aria-label':'Container status','aria-controls':'container-log-panel','aria-expanded':'false',title:'Container: waiting — Show logs',onclick:e=>{buildPanel.hidden=!buildPanel.hidden;e.currentTarget.setAttribute('aria-expanded',String(!buildPanel.hidden));}},controlIcon('M12 3 3 8v9l9 5 9-5V8z M3 8l9 5 9-5 M12 13v9 M7.5 5.5l9 5'),h('span',{class:'build-spinner','aria-hidden':true})));

  const status=h('span',{});
  const notice=h('div',{class:'error',role:'status'});
  const cancel=h('button',{class:'toolbar-button cancel-control','aria-label':'Cancel eval',title:'Cancel evaluation',onclick:async()=>{cancel.disabled=true;try{const res=await fetch(`/api/jobs/${id}/cancel`,{method:'POST'});if(!res.ok)throw new Error((await res.json()).error);}catch(e){notice.textContent=e.message;cancel.disabled=false;}}},controlIcon('M8 3h8l5 5v8l-5 5H8l-5-5V8z M9 9h6v6H9z'));
  const controls=h('div',{class:'eval-controls',role:'group','aria-label':'Evaluation controls'},cancel,build,buildPanel);
  let follows=true,lastScroll=0;
  const atBottom=()=>feed.scrollHeight-feed.clientHeight-feed.scrollTop<=4;
  feed.tabIndex=0;
  feed.setAttribute('aria-label','Agent activity');
  feed.addEventListener('wheel',e=>{if(e.deltaY<0)follows=false;},{passive:true});
  feed.addEventListener('keydown',e=>{if(['ArrowUp','PageUp','Home'].includes(e.key))follows=false;});
  feed.addEventListener('scroll',()=>{
    // Our render restores scrollTop before this asynchronous event fires.
    // Only a changed position represents the reader moving through the log.
    if(feed.scrollTop!==lastScroll){follows=atBottom();lastScroll=feed.scrollTop;}
  },{passive:true});
  const elapsed=t=>t.started_at?dur(((t.finished_at?new Date(t.finished_at):new Date())-new Date(t.started_at))*1e6):'';
  const selectTask=t=>{selected=t.id;pinnedProblem=true;agentFilter='';problems.setExpanded(false);problemSummary.focus();follows=true;drawTable();drawTree();};
  function drawTable() {
    drawStats();
    const done=snap.tasks.filter(t=>t.phase==='finished');
    mount(status,snap.status==='running'?h('span',{class:'pulse'}):null,h('b',{},snap.status),` · ${done.length}/${snap.tasks.length} finished · ${done.filter(t=>t.passed).length} passed`,snap.error?h('span',{class:'error'},` · ${snap.error}`):null);
    cancel.hidden=snap.status!=='running';
    const active=snap.tasks.find(t=>['running','starting','grading'].includes(t.phase));
    if(!pinnedProblem && active && selected!==active.id){selected=active.id;agentFilter='';drawTree();drawStats();}
    const chosen=snap.tasks.find(t=>t.id===selected);
    mount(problemSummary,h('span',{class:'header-problem-text'},h('span',{class:'problem-title'},chosen?.title||chosen?.id||'No problems'),h('progress',{class:'header-progress',max:snap.tasks.length||1,value:done.length,'aria-label':'Overall progress'})),h('span',{class:'header-problem-count'},`${done.length}/${snap.tasks.length}`),h('span',{class:'problem-chevron','aria-hidden':true},'⌄'));
    mount(overview,h('div',{class:'problem-overview-heading'},h('h3',{},'Problem set'),h('span',{class:'muted'},`${done.length} of ${snap.tasks.length} finished`)),h('progress',{max:snap.tasks.length||1,value:done.length,'aria-label':'Evaluation completion'}),status,h('div',{class:'muted'},snap.metadata?.model?.model||'', ' · ',configLabel(snap.metadata?.model)),h('div',{class:'muted'},'Started ',when(snap.started_at)));
    for(const t of snap.tasks){
      let row=problemRows.get(t.id);
      if(!row){row=h('button',{type:'button',class:'problem-option',onclick:()=>selectTask(t)});problemRows.set(t.id,row);table.append(row);}
      row.classList.toggle('selected',selected===t.id);
      row.setAttribute('aria-pressed',String(selected===t.id));
      mount(row,h('span',{class:'problem-option-check','aria-hidden':true},selected===t.id?'✓':''),h('span',{class:'problem-option-text'},h('span',{},t.title||t.id),h('span',{class:'muted'},t.id)),h('span',{class:'problem-option-state'},t.outcome?outcomeTag(t):h('span',{class:`phase ${t.phase}`},t.phase),h('span',{class:'muted'},elapsed(t))));
    }
    if(!snap.tasks.length)mount(table,h('div',{class:'empty'},'No problems in this evaluation.'));
  }

  function contextLabel(agent) {
    const context=snap.tasks.find(t=>t.id===selected)?.context?.[agent];
    return !context||context.error?'Context not available':`${num(context.tokens)} context tokens`;
  }
  function drawStats() {
    const task=snap.tasks.find(t=>t.id===selected);
    const metrics=[['Elapsed',task?.started_at?elapsed(task):'—','Wall time'],['Model calls',num(task?.model_calls),'Completed calls'],['Tool calls',num(task?.tool_calls),'Completed calls'],['Tool errors',num(task?.tool_errors),'Failed tool calls'],['Input tokens',num(task?.input_tokens),'Across all calls'],['Output tokens',num(task?.output_tokens),'Across all calls']];
    mount(stats,metrics.map(([label,value,hint])=>h('div',{class:'stat-card'+(label==='Tool errors'&&task?.tool_errors?' has-errors':''),'data-metric':label},h('span',{class:'stat-label'},label),h('strong',{class:'stat-value'},value),h('span',{class:'stat-hint'},hint))));
    const contexts=Object.keys(task?.context||{}).filter(agent=>!agentFilter||agentFilter===agent);
    mount(contextPanel,h('div',{class:'eyebrow'},'Context per agent'),h('p',{class:'muted'},'Latest measured context'),contexts.length?contexts.map(agent=>h('div',{class:'context-entry'},h('span',{},agent),h('span',{'data-context-agent':agent},contextLabel(agent)))):h('p',{class:'muted'},'No context measurements yet.'));
  }
  function drawTree() {
    const scroll=feed.scrollTop;
    const items=events.filter(e=>e.task===selected);
    const agents=new Map();
    for(const e of items)if(e.agent&&!agents.has(e.agent))agents.set(e.agent,e.parent||'');else if(e.agent&&e.parent)agents.set(e.agent,e.parent);
    // Resolve only recorded parent relationships. Chronology never determines nesting.
    const parents=new Map();
    for(const [agent,parent] of agents) {
      let p=parent;const ancestors=new Set([agent]);let cycle=false;
      while(p&&agents.has(p)){if(ancestors.has(p)){cycle=true;break;}ancestors.add(p);p=agents.get(p);}
      parents.set(agent,!cycle&&agents.has(parent)?parent:'');
    }
    const agentButton=(agent,label)=>h('button',{class:'agent-button'+(agentFilter===agent?' active':''),'aria-pressed':agentFilter===agent,onclick:()=>{agentFilter=agent;follows=true;drawTree();}},label);
    function branch(parent) {
      return h('ul',{},[...parents].filter(([,p])=>p===parent).map(([agent])=>h('li',{'data-agent':agent},agentButton(agent,agent),h('span',{class:'agent-parent'},parents.get(agent)?`Child of ${parents.get(agent)}`:'Root agent'),branch(agent))));
    }
    mount(tree,h('div',{class:'eyebrow'},'Agents'),agentButton('','All agents'),branch(''));
    const visible=items.filter(e=>!agentFilter||e.agent===agentFilter);
    mount(feed,h('div',{class:'log-columns','aria-hidden':'true'},['Time','Agent','Action','Tool','Contents'].map(label=>h('span',{},label))),visible.map(e=>{
      const isTool=['tool','tool_done','tool_error'].includes(e.kind);
      const text=e.text||'';
      const match=isTool?/^([^\s:]+)(?:[ :]|\n|$)/.exec(text):null;
      const tool=match?.[1]||'';
      // Existing stored events encode the tool in the first line. Remove only
      // that prefix, retaining arguments, output, errors and duration verbatim.
      const body=tool?text.slice(tool.length).replace(/^: ?/,'').trim():text;
      const outcome=e.kind==='tool_done'?'Success':e.kind==='tool_error'?'Failed':'';
      const action=isTool?'Tool':e.kind==='phase'?e.phase:e.kind.replaceAll('_',' ');
      return h('div',{class:`line ${e.kind}`,'data-event-seq':e.seq},h('time',{class:'t',datetime:e.at},new Date(e.at).toLocaleTimeString('en-US',{hour12:false})),h('span',{class:'event-agent'},e.agent||'Problem'),h('span',{class:'k'},action),h('div',{class:'event-tool'},h('span',{class:'compact-action'},action),h('code',{},tool||'—'),outcome?h('span',{class:'tool-outcome '+(e.kind==='tool_error'?'failed':'passed')},outcome):null),h('div',{class:'x'},markdown(body)));
    }));
    drawStats();
    const failures=items.filter(e=>e.kind==='tool_error'||e.kind==='error');
    attention.hidden=!failures.length;
    mount(attention,h('div',{class:'eyebrow'},'Needs attention'),failures.map(e=>h('button',{class:'failure-shortcut',onclick:()=>{agentFilter=e.agent||'';follows=false;drawTree();const row=feed.querySelector(`[data-event-seq="${e.seq}"]`);if(row){feed.scrollTop=row.offsetTop-feed.offsetTop-32;lastScroll=feed.scrollTop;row.classList.add('highlighted');}}},h('span',{},clipText(e.text?.split('\n')[0]||'Error',100)),h('small',{},e.agent||'Problem',' · ',new Date(e.at).toLocaleTimeString('en-US',{hour12:false})))));
    if(!visible.length)feed.append(h('div',{class:'empty',style:'padding:18px'},'Waiting for activity…'));
    const builds=events.filter(e=>e.kind==='build');
    if(builds.length){
      const latest=builds[builds.length-1];
      buildLabel.textContent=latest.phase==='failed'?'Build failed':latest.phase==='ready'?'Image ready':latest.phase==='preparing'?'Preparing…':'Building…';
      build.firstChild.title=`Container: ${buildLabel.textContent} — Show logs`;
      build.classList.toggle('is-building',latest.phase==='building'||latest.phase==='preparing');
      build.classList.toggle('build-failed',latest.phase==='failed');
      build.classList.toggle('build-ready',latest.phase==='ready');
      buildOutput.textContent=builds.map(e=>e.text).join('\n');
    }
    feed.scrollTop=follows?feed.scrollHeight:scroll;
    lastScroll=feed.scrollTop;
  }
  drawTable();drawTree();
  $('#toolbar-title').textContent=snap.name;
  $('#toolbar-title').title=snap.name;
  $('#toolbar-title').hidden=false;
  $('#toolbar-actions').before(problems);
  mount($('#toolbar-actions'));
  mount(main,notice,stats,h('div',{class:'progress-layout'},h('div',{class:'agent-column'},controls,tree,contextPanel,attention),feed));
  const source=new EventSource(`/api/jobs/${id}/events`);
  const tick=setInterval(drawTable,1000);
  let pending=false;
  const stop=()=>{source.close();clearInterval(tick);foldAnimation?.cancel();};
  disposeView=stop;
  source.addEventListener('progress',ev=>{
    const e=JSON.parse(ev.data);if(seen.has(e.seq))return;seen.add(e.seq);events.push(e);
    if(!pending){pending=true;requestAnimationFrame(()=>{pending=false;if(feed.isConnected)drawTree();});}
  });
  source.addEventListener('state',ev=>{snap=JSON.parse(ev.data);drawTable();refreshRunning();});
  source.addEventListener('end',async()=>{stop();snap=await api(`/api/jobs/${id}`);drawTable();await loadRuns(true);await refreshRunning();});
  source.onerror=()=>{if(snap.status!=='running')stop();};
  window.addEventListener('hashchange',stop,{once:true});
}
function clipText(s, n) { return s.length > n ? s.slice(0, n) + '…' : s; }

// Wiring
document.addEventListener('click',e=>document.querySelectorAll('.row-menu[open], .problem-dropdown[open]').forEach(menu=>{if(!menu.contains(e.target))menu.setExpanded?menu.setExpanded(false):menu.open=false;}));
document.addEventListener('keydown',e=>{if(e.key==='Escape')document.querySelectorAll('.row-menu[open], .problem-dropdown[open]').forEach(menu=>{menu.setExpanded?menu.setExpanded(false):menu.open=false;menu.firstChild.focus();});});
const gun=['........................O...........','......................O.O...........','............OO......OO............OO','...........O...O....OO............OO','OO........O.....O...OO..............','OO........O...O.OO....O.O...........','..........O.....O.......O...........','...........O...O....................','............OO......................'];
gun.forEach((row,y)=>[...row].forEach((cell,x)=>{if(cell==='O'){const dot=document.createElementNS('http://www.w3.org/2000/svg','rect');for(const [key,value] of Object.entries({x:x+1,y:y+1,width:.85,height:.85,fill:'currentColor'}))dot.setAttribute(key,value);$('#strap-mark').append(dot);}}));
function setSidebar(hidden) {
  $('.app').classList.toggle('sidebar-hidden',hidden);
  $('#sidebar').inert=hidden;
  $('#sidebar').setAttribute('aria-hidden',String(hidden));
  sidebarHidden=hidden;
  const toggle=$('#sidebar-toggle');
  toggle.setAttribute('aria-expanded',String(!hidden));
  toggle.setAttribute('aria-label',hidden?'Show sidebar':'Hide sidebar');
  toggle.title=hidden?'Show sidebar':'Hide sidebar';
  try { localStorage.setItem('eval-sidebar-hidden',String(hidden)); } catch {}
}
let sidebarHidden=false;
try { sidebarHidden=localStorage.getItem('eval-sidebar-hidden')==='true'; } catch {}
applyAppearance(preference('appearance','system'));
setSidebar(sidebarHidden);
$('#sidebar-toggle').addEventListener('click',()=>setSidebar(!sidebarHidden));
window.addEventListener('hashchange', render);
$('#refresh').addEventListener('click', async e => {const button=e.currentTarget;button.disabled=true;try{await loadRuns(true);await render();}catch(err){alert(err.message);}finally{button.disabled=false;}});
Promise.all([loadRuns(false), loadRunner()]).then(() => {
  const r = route();
  if (r.view === 'compare') for (const p of r.runs) state.selected.add(p);
  updateCompare();
  render();
}).catch(err => $('#main').replaceChildren(h('div', { class: 'empty error' }, err.message)));
