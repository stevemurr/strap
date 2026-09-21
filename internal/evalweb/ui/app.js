// strap eval results: one page over the /api endpoints. Hash routes:
//   #/                          welcome
//   #/run/<path>                run overview and tasks
//   #/run/<path>/task/<id>      one task with its trace
//   #/run/<path>/trial/<scenario>/<n>   one interaction trial
//   #/compare?runs=a|b|c        outcome matrix across ladder runs
'use strict';

const state = { runs: [], root: '', filter: '', selected: new Set(), open: new Set(), sort: { key: 'task_id', dir: 1 } };
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
  const root = $('#root');
  root.textContent = data.root;
  root.title = data.root;
  renderRuns();
}

function renderRuns() {
  const list = $('#runs');
  list.replaceChildren();
  const current = route();
  const needle = state.filter.trim().toLowerCase();
  const runs = state.runs.filter(r => !needle || [r.path, r.model, r.profile, r.commit].join(' ').toLowerCase().includes(needle));
  if (!runs.length) {
    list.append(h('div', { class: 'empty', style: 'padding:20px 14px' }, state.runs.length ? 'No runs match the filter.' : 'No runs found. A run is any directory holding results.jsonl.'));
    return;
  }
  const groups = new Map();
  const batches = new Map();
  for (const r of runs) {
    if (r.batch) { batches.set(r.path, r); continue; }
    const key = r.group || 'runs';
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(r);
  }
  // A batch whose members are all filtered out still shows when it matches.
  for (const [p, b] of batches) if (!groups.has(p)) groups.set(p, []);
  // Nested groups (container batches, comparison bundles, sweeps) start
  // folded to one line with their totals; the group holding the current run
  // is always open.
  for (const [name, members] of groups) {
    const nested = name !== 'runs';
    const batch = batches.get(name);
    const holdsCurrent = members.some(r => r.path === current.run || (current.view === 'compare' && current.runs.includes(r.path)));
    const open = !nested || needle || holdsCurrent || state.open.has(name);
    const ladder = members.filter(r => r.kind === 'ladder');
    const totals = batch ? `${batch.passed}/${batch.tasks}` : ladder.length ? `${ladder.reduce((a, r) => a + r.passed, 0)}/${ladder.reduce((a, r) => a + r.tasks, 0)}` : '';
    const toggle = () => { if (!nested) return; open && !holdsCurrent ? state.open.delete(name) : state.open.add(name); renderRuns(); };
    if (batch) {
      // A batch reads as one run: the row opens the merged view and can be
      // ticked for comparison; the arrow lists the attempts beneath it.
      const box = h('input', { type: 'checkbox', title: 'Select for comparison', onclick: e => e.stopPropagation(), onchange: e => { e.target.checked ? state.selected.add(name) : state.selected.delete(name); updateCompare(); } });
      box.checked = state.selected.has(name);
      const isActive = current.run === name || (current.view === 'compare' && current.runs.includes(name));
      list.append(h('div', { class: 'group batch' + (isActive ? ' active' : ''), role: 'link', tabindex: 0, onclick: () => location.hash = runHref(name), onkeydown: e => { if (e.key === 'Enter') location.hash = runHref(name); } },
        box, h('span', {}, h('span', { class: 'name', title: name }, name), h('div', { class: 'meta' }, [batch.model, short(batch.commit), `${batch.members} attempt${batch.members === 1 ? '' : 's'}`].filter(Boolean).join(' · ')),
          batch.tasks ? h('div', { class: 'bar' }, h('i', { style: `width:${100 * batch.passed / batch.tasks}%` })) : null),
        h('span', {}, h('div', { class: 'score' }, totals), h('button', { class: 'arrow-btn', title: open ? 'Hide attempts' : 'Show attempts', onclick: e => { e.stopPropagation(); toggle(); } }, open ? '▾' : '▸'))));
    } else {
      list.append(h('div', { class: 'group', role: 'button', tabindex: 0, onclick: toggle },
        h('span', { class: 'arrow' }, nested ? (open ? '▾' : '▸') : ''), h('span', { class: 'name', title: name }, name, h('span', { style: 'color:var(--muted)' }, ` ${members.length}`)), h('span', { class: 'score' }, totals)));
    }
    if (open) for (const r of members) list.append(runRow(r, current));
  }
}

function runRow(r, current) {
  const active = (current.run === r.path) || (current.view === 'compare' && current.runs.includes(r.path));
  const box = h('input', { type: 'checkbox', title: 'Select for comparison', disabled: r.kind !== 'ladder' || null, onclick: e => e.stopPropagation(), onchange: e => { e.target.checked ? state.selected.add(r.path) : state.selected.delete(r.path); updateCompare(); } });
  box.checked = state.selected.has(r.path);
  const meta = r.kind === 'interaction'
    ? `interaction · ${r.trials?.mode || ''} · ${r.profile || ''}`
    : [r.model, r.profile !== r.model ? r.profile : null, short(r.commit)].filter(Boolean).join(' · ');
  const score = r.kind === 'interaction'
    ? h('div', { class: 'score interaction' }, r.trials ? `${r.trials.passed}/${r.trials.completed}` : '?')
    : h('div', { class: 'score' }, `${r.passed}/${r.tasks}`);
  const row = h('div', { class: 'run' + (active ? ' active' : ''), role: 'link', tabindex: 0, onclick: () => location.hash = runHref(r.path), onkeydown: e => { if (e.key === 'Enter') location.hash = runHref(r.path); } },
    box,
    h('div', {}, h('div', { class: 'name', title: r.path }, r.name), h('div', { class: 'meta' }, r.error ? h('span', { class: 'error' }, 'unreadable') : meta),
      r.kind === 'ladder' && r.tasks ? h('div', { class: 'bar' }, h('i', { style: `width:${100 * r.passed / r.tasks}%` })) : null),
    score);
  return row;
}

function updateCompare() {
  const btn = $('#compare-btn');
  const n = state.selected.size;
  btn.hidden = n < 2;
  btn.textContent = `Compare ${n} runs`;
  btn.className = 'button primary';
}

// Main views
async function render() {
  const main = $('#main');
  const r = route();
  renderRuns();
  try {
    if (r.view === 'home') return renderHome(main);
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

function renderHome(main) {
  const ladder = state.runs.filter(r => r.kind === 'ladder'), inter = state.runs.filter(r => r.kind === 'interaction');
  mount(main, 
    h('div', { class: 'head' }, h('h2', {}, 'Results')),
    h('div', { class: 'facts' }, h('span', {}, h('b', {}, num(ladder.length)), ' ladder runs'), h('span', {}, h('b', {}, num(inter.length)), ' interaction runs'), h('span', {}, 'under ', h('code', {}, state.root))),
    h('div', { class: 'empty' }, 'Pick a run on the left to read it. Tick two or more ladder runs to compare their outcomes task by task. Every number here is derived from results.jsonl and the traces, so runs without a written report are complete too.'),
  );
}

function header(summary, extra = []) {
  const s = summary;
  return [
    h('div', { class: 'head' }, h('h2', {}, s.path)),
    h('div', { class: 'facts' },
      s.model ? h('span', {}, 'model ', h('b', {}, s.model)) : null,
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
      return line(m.from ? h('span', { class: 'flag', style: 'margin:0 6px 0 0' }, `${m.from} → ${m.to}`) : null, h('pre', {}, String(m.content ?? p.content ?? '').slice(0, 800)));
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
  if (paths.length < 2) { mount(main, h('div', { class: 'empty' }, 'Tick at least two ladder runs on the left, then press Compare.')); return; }
  mount(main, h('div', { class: 'empty' }, `Analysing ${paths.length} runs…`));
  const data = await api('/api/compare?' + paths.map(p => `run=${encodeURIComponent(p)}`).join('&'));
  const runs = data.runs;
  const tasks = new Map();
  for (const r of runs) for (const t of r.report.tasks) if (!tasks.has(t.task_id)) tasks.set(t.task_id, t.tier);
  const tiers = ['easy', 'medium', 'hard'];
  const ordered = [...tasks].sort((a, b) => (tiers.indexOf(a[1]) - tiers.indexOf(b[1])) || a[0].localeCompare(b[0]));
  const at = (r, id) => r.report.tasks.find(t => t.task_id === id);
  const label = r => r.summary.name + (r.summary.group ? ` (${r.summary.group})` : '');
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

// Launching runs. The server runs tasks on this host with the model it was
// started with and streams progress over server-sent events.
let runner = null;
async function loadRunner() {
  try { runner = await api('/api/runner'); } catch { runner = { enabled: false }; }
  $('#launch').hidden = !runner.enabled;
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
    start.disabled = true;
    note.textContent = 'Starting…';
    try {
      const res = await fetch('/api/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ tasks: [...chosen] }) });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error || res.statusText);
      location.hash = `#/job/${body.id}`;
    } catch (err) { note.textContent = err.message; start.disabled = false; }
  });
  mount(main, 
    h('div', { class: 'head' }, h('h2', {}, 'Run evals')),
    h('div', { class: 'facts' }, h('span', {}, 'model ', h('b', {}, runner.model)), h('span', {}, 'backend ', h('b', {}, runner.backend)), h('span', {}, 'endpoint ', h('code', {}, runner.base_url)), runner.profile ? h('span', {}, 'profile ', h('b', {}, runner.profile)) : null, h('span', {}, 'ladder ', h('code', {}, runner.ladder))),
    runner.error ? h('div', { class: 'error' }, runner.error) : null,
    h('div', { class: 'empty', style: 'padding:0 0 12px' }, 'Tasks run one after another on this machine, each in a fresh workspace, and are graded with the hidden tests as they finish. Results land in a new batch under the results directory.'),
    h('div', { class: 'launch-bar' }, start, note),
    picker,
  );
}

async function renderJob(main, id) {
  let snap = await api(`/api/jobs/${id}`);
  const table = h('table', { class: 'dense' });
  const feed = h('div', { class: 'feed' });
  const status = h('span', {});
  const cancel = h('button', { class: 'button', onclick: async () => { cancel.disabled = true; await fetch(`/api/jobs/${id}/cancel`, { method: 'POST' }); } }, 'Cancel');
  const follow = h('label', { class: 'flag' }, h('input', { type: 'checkbox', checked: true }), ' follow');
  let follows = true;
  follow.firstChild.addEventListener('change', e => follows = e.target.checked);
  const elapsed = t => t.started_at ? dur(((t.finished_at ? new Date(t.finished_at) : new Date()) - new Date(t.started_at)) * 1e6) : '';
  function drawTable() {
    const done = snap.tasks.filter(t => t.phase === 'finished');
    const passed = done.filter(t => t.passed).length;
    mount(status, snap.status === 'running' ? h('span', { class: 'pulse' }) : null, h('b', {}, snap.status), ` · ${done.length}/${snap.tasks.length} finished · ${passed} passed`, snap.error ? h('span', { class: 'error' }, ` · ${snap.error}`) : null);
    cancel.hidden = snap.status !== 'running';
    table.replaceChildren(
      h('thead', {}, h('tr', {}, ['task', 'phase', 'outcome', 'time', 'calls', 'tools', 'tool errs', 'agents', 'tokens in', 'tokens out', ''].map((l, i) => h('th', { class: i >= 3 ? 'num' : '' }, l)))),
      h('tbody', {}, snap.tasks.map(t => h('tr', {},
        h('td', { class: 'id', title: t.title }, t.id),
        h('td', {}, h('span', { class: `phase ${t.phase}` }, t.phase)),
        h('td', {}, t.outcome ? outcomeTag(t) : '', t.error ? h('div', { class: 'flag error', style: 'margin:0' }, clipText(t.error, 120)) : null),
        h('td', { class: 'num' }, elapsed(t)), h('td', { class: 'num' }, num(t.model_calls)), h('td', { class: 'num' }, num(t.tool_calls)), h('td', { class: 'num' }, num(t.tool_errors)), h('td', { class: 'num' }, num(t.agents)), h('td', { class: 'num' }, num(t.input_tokens)), h('td', { class: 'num' }, num(t.output_tokens)),
        h('td', {}, t.results ? h('a', { href: runHref(t.results) }, 'open') : '')))));
  }
  function addLine(e) {
    const line = h('div', { class: `line ${e.kind}` }, h('div', { class: 't' }, new Date(e.at).toLocaleTimeString('en-US', { hour12: false })), h('div', { class: 'task' }, e.task || ''), h('div', { class: 'k' }, e.kind === 'phase' ? e.phase : e.kind, e.agent ? ` ${e.agent}` : ''), h('div', { class: 'x' }, e.text || ''));
    feed.append(line);
    while (feed.children.length > 400) feed.firstChild.remove();
    if (follows) feed.scrollTop = feed.scrollHeight;
  }
  drawTable();
  mount(main, 
    h('div', { class: 'head' }, h('h2', {}, snap.name), h('a', { href: '#/launch' }, 'run more')),
    h('div', { class: 'facts' }, status, h('span', {}, 'started ', h('b', {}, when(snap.started_at))), h('span', {}, 'results in ', h('code', {}, snap.dir))),
    h('div', { class: 'launch-bar' }, cancel, follow),
    h('div', { class: 'section' }, table),
    h('div', { class: 'section' }, h('h3', {}, 'Progress'), feed),
  );
  const source = new EventSource(`/api/jobs/${id}/events`);
  const tick = setInterval(drawTable, 1000);
  const stop = () => { source.close(); clearInterval(tick); };
  source.addEventListener('progress', ev => addLine(JSON.parse(ev.data)));
  source.addEventListener('state', ev => { snap = JSON.parse(ev.data); drawTable(); refreshRunning(); });
  source.addEventListener('end', async () => { stop(); snap = await api(`/api/jobs/${id}`); drawTable(); await loadRuns(true); await refreshRunning(); });
  source.onerror = () => { if (snap.status !== 'running') stop(); };
  window.addEventListener('hashchange', stop, { once: true });
}
function clipText(s, n) { return s.length > n ? s.slice(0, n) + '…' : s; }

// Wiring
window.addEventListener('hashchange', render);
$('#filter').addEventListener('input', e => { state.filter = e.target.value; renderRuns(); });
$('#compare-btn').addEventListener('click', () => location.hash = compareHref([...state.selected]));
$('#refresh').addEventListener('click', () => loadRuns(true).then(render));
Promise.all([loadRuns(false), loadRunner()]).then(() => {
  const r = route();
  if (r.view === 'compare') for (const p of r.runs) state.selected.add(p);
  updateCompare();
  render();
}).catch(err => $('#main').replaceChildren(h('div', { class: 'empty error' }, err.message)));
