/* The desk: a page over /api. The token is kept in this browser; the
   server says who it belongs to. Notes are grouped by thread; new ones
   arrive over the event stream. */
(() => {
  'use strict';
  const $ = (sel, el = document) => el.querySelector(sel);
  const h = (tag, attrs = {}, ...kids) => {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (k === 'class') el.className = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (v !== null && v !== undefined) el.setAttribute(k, v);
    }
    for (const kid of kids.flat()) if (kid !== null && kid !== undefined) el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
    return el;
  };
  let token = '';
  try { token = localStorage.getItem('desk.token') || ''; } catch (e) {}
  const state = { me: '', control: {}, notes: [], seq: 0, seen: {}, open: {}, thread: '', filter: 'all', es: null,
    view: 'notes', calls: [], journalPort: 8790, call: '', callsAt: 0,
    scenarios: [], scenario: '', edit: null, editName: '', realCalls: [], scenariosAt: 0 };

  const api = async (path, opts = {}) => {
    const r = await fetch(path, { ...opts, headers: { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json', ...(opts.headers || {}) } });
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw Object.assign(new Error(body.error || ('HTTP ' + r.status)), { status: r.status });
    return body;
  };
  const tip = (text, bad) => {
    const el = $('#tip'); el.textContent = text; el.hidden = false; el.style.background = bad ? 'var(--over)' : '';
    clearTimeout(tip.t); tip.t = setTimeout(() => { el.hidden = true; }, 3500);
  };
  const ago = (iso) => {
    if (!iso) return 'never';
    const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return Math.floor(s / 60) + ' min ago';
    if (s < 86400) return Math.floor(s / 3600) + ' h ago';
    return Math.floor(s / 86400) + ' d ago';
  };
  const when = (iso) => { const d = new Date(iso); return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }); };

  const gate = () => {
    $('#main').replaceChildren(h('div', { class: 'card gate' },
      h('h1', {}, 'The desk'),
      h('p', { class: 'sub' }, 'Where Claude and Codex leave each other notes, with the owner watching. Paste your token: owner.token, claude.token or codex.token on the box.'),
      h('label', {}, 'Token'),
      h('input', { type: 'text', id: 'tok', autocomplete: 'off' }),
      h('div', { class: 'row' }, h('button', { class: 'btn btn-primary', onclick: async () => {
        token = $('#tok').value.trim();
        try { localStorage.setItem('desk.token', token); } catch (e) {}
        await start();
      } }, 'Open'))));
  };
  $('#token-btn').addEventListener('click', () => { if (state.es) state.es.close(); state.es = null; gate(); });

  const start = async () => {
    try {
      const me = await api('/api/me');
      state.me = me.who; state.control = me.control;
    } catch (e) { tip(e.message, true); gate(); return; }
    await refresh();
    listen();
    render();
  };
  const refresh = async () => {
    const [st, ns] = await Promise.all([api('/api/state'), api('/api/notes')]);
    state.control = st.control; state.seen = st.seen; state.open = st.open;
    state.notes = ns.notes || []; state.seq = ns.seq;
  };
  const listen = () => {
    if (state.es) state.es.close();
    // EventSource cannot send a header; the stream is fetched instead.
    const ctl = new AbortController();
    state.es = { close: () => ctl.abort() };
    (async () => {
      try {
        const r = await fetch('/api/events', { headers: { 'Authorization': 'Bearer ' + token }, signal: ctl.signal });
        const reader = r.body.getReader(); const dec = new TextDecoder(); let buf = '';
        for (;;) {
          const { value, done } = await reader.read(); if (done) break;
          buf += dec.decode(value, { stream: true });
          let i;
          while ((i = buf.indexOf('\n\n')) >= 0) {
            const chunk = buf.slice(0, i); buf = buf.slice(i + 2);
            let ev = '', data = '';
            for (const line of chunk.split('\n')) { if (line.startsWith('event: ')) ev = line.slice(7); else if (line.startsWith('data: ')) data += line.slice(6); }
            if (ev === 'note') { const n = JSON.parse(data); if (!state.notes.some(x => x.id === n.id)) state.notes.push(n); state.seq = n.seq; }
            else if (ev === 'done' || ev === 'control') { await refresh(); }
            if (ev) render();
          }
        }
      } catch (e) { if (!ctl.signal.aborted) setTimeout(listen, 3000); }
    })();
  };

  const threads = () => {
    const m = new Map();
    for (const n of state.notes) {
      const t = m.get(n.thread) || { id: n.thread, notes: [] };
      t.notes.push(n); m.set(n.thread, t);
    }
    return [...m.values()].sort((a, b) => b.notes[b.notes.length - 1].seq - a.notes[a.notes.length - 1].seq);
  };
  const matches = (t) => {
    if (state.filter === 'all') return true;
    if (state.filter === 'open') return t.notes.some(n => n.open);
    if (state.filter === 'me') return t.notes.some(n => n.to === state.me || n.from === state.me);
    return t.notes.some(n => n.from === state.filter);
  };

  // The page: notes, or the calls placed to the simulated customer.
  const route = () => {
    const p = (location.hash || '#/').replace(/^#\/?/, '').split('/').filter(Boolean);
    state.view = p[0] === 'calls' ? 'calls' : p[0] === 'scenarios' ? 'scenarios' : 'notes';
    if (state.view === 'calls' && p[1]) state.call = decodeURIComponent(p[1]);
    if (state.view === 'scenarios' && p[1]) state.scenario = decodeURIComponent(p[1]);
    if (state.me) render();
  };
  window.addEventListener('hashchange', route);
  route();

  const loadCalls = async () => {
    const d = await api('/api/calls');
    state.calls = d.calls || []; state.journalPort = d.journal_port || 8790; state.callsAt = Date.now();
    if (!state.call && state.calls.length) state.call = state.calls[0].call;
  };
  const dur = (s) => !s ? '–' : s < 60 ? Math.round(s) + ' s' : Math.floor(s / 60) + ' min ' + String(Math.round(s % 60)).padStart(2, '0') + ' s';
  const phaseChip = (r) => {
    const bad = (r.failures && r.failures.length) || r.phase === 'failed' || r.phase === 'expired';
    const live = ['prepared', 'waiting', 'ringing', 'answering', 'connected', 'ending'].includes(r.phase);
    return h('span', { class: 'chip' + (bad ? ' warn' : live ? '' : ' good') }, r.phase || '?');
  };
  const renderCalls = () => {
    const rows = state.calls;
    const r = rows.find(x => x.call === state.call) || rows[0];
    if (r && r.call !== state.call) state.call = r.call;
    const left = h('div', {},
      h('div', { class: 'row' }, h('button', { class: 'btn', onclick: async () => { try { await loadCalls(); render(); } catch (e) { tip(e.message, true); } } }, 'Refresh'),
        h('span', { class: 'muted' }, rows.length + (rows.length === 1 ? ' simulated call' : ' simulated calls'))),
      h('div', { class: 'threads' }, rows.length ? rows.map(x =>
        h('button', { 'aria-current': String(state.call === x.call), onclick: () => { state.call = x.call; location.hash = '#/calls/' + encodeURIComponent(x.call); render(); } },
          h('span', { class: 't-subject' }, when(x.at), ' ', phaseChip(x), x.has_audio ? h('span', { class: 'muted' }, ' ▶ recording') : null),
          h('span', { class: 't-meta' }, (x.scenario || 'scenario ?') + ' · run ' + x.run.slice(0, 10) + '… · ' + (x.journal ? x.turns + ' turns, p50 ' + (x.p50 || '–') + ' ms' : 'not in the journal'))))
        : h('div', { class: 'empty' }, 'no simulated call yet: scripts/sim-brain.sh prepare <scenario>, then scripts/phone-brain.sh call sim')));
    const right = h('div', {});
    if (r) {
      const checks = r.checks ? Object.entries(r.checks) : [];
      const gaps = (r.gaps_ms || []).slice().sort((a, b) => a - b);
      const q = (p) => gaps.length ? gaps[Math.min(gaps.length - 1, Math.floor(p * gaps.length))] : null;
      right.append(
        h('div', { class: 'card' },
          h('h2', { style: 'margin-top:0' }, 'Simulated call of ', when(r.at), ' ', h('span', { class: 'sim-badge' }, 'sim'), ' ', phaseChip(r)),
          h('dl', { class: 'facts' },
            h('dt', {}, 'Run'), h('dd', {}, h('span', { class: 'mono' }, r.run)),
            h('dt', {}, 'Call'), h('dd', {}, h('span', { class: 'mono' }, r.call), r.build ? [' on ', h('span', { class: 'mono' }, r.build)] : null),
            h('dt', {}, 'Scenario'), h('dd', {}, r.scenario || '–'),
            h('dt', {}, 'Ended'), h('dd', {}, r.ended || (r.phase === 'completed' ? 'cleanly' : '–')),
            h('dt', {}, 'golive'), h('dd', {}, r.journal ? [dur(r.duration_s), ', ', r.turns, ' turns, p50 ', r.p50 || '–', ' ms, ', (r.turns >= 40 ? 'p95 ' : 'worst '), r.p95 || '–', ' ms, ', r.over, ' over 1200'] : 'the journal has no call of this id'),
            h('dt', {}, 'Customer\u2019s view'), h('dd', {}, gaps.length ? ['assistant response gaps (its audio boundary): ', gaps.length, ' valid, p50 ', q(0.5), ' ms, worst ', gaps[gaps.length - 1], ' ms'] : 'no valid response gap measured'),
            h('dt', {}, 'Checks'), h('dd', {}, checks.length ? checks.map(([k, v]) => h('span', { class: 'chip ' + (v === 'pass' ? 'good' : v === 'fail' ? 'warn' : ''), title: v }, k + ' · ' + v)) : '–'),
            h('dt', {}, 'Failures'), h('dd', {}, r.failures && r.failures.length ? r.failures.join('; ') : 'none')),
          h('div', { class: 'row' },
            h('a', { class: 'btn', href: '/api/calls/' + encodeURIComponent(r.run) + '/report', target: '_blank', rel: 'noopener', onclick: (e) => {
              e.preventDefault();
              api('/api/calls/' + encodeURIComponent(r.run) + '/report').then(d => { const w = window.open('', '_blank'); w.document.write('<pre>' + JSON.stringify(d, null, 2).replace(/</g, '&lt;') + '</pre>'); }).catch(err => tip(err.message, true));
            } }, 'Full report'),
            r.journal ? h('a', { class: 'btn', href: journalURL(r.call), target: '_blank', rel: 'noopener' }, 'Open in the journal') : null)),
        r.journal ? h('div', { class: 'card embed' },
          h('p', { class: 'sub' }, 'The journal\u2019s view of the same call: the recording (customer left, assistant right), the turns, the gate.'),
          h('iframe', { src: journalURL(r.call), title: 'The call journal', loading: 'lazy' })) : null);
    } else {
      right.append(h('div', { class: 'empty' }, 'nothing to show yet'));
    }
    $('#main').replaceChildren(h('div', { class: 'desk' }, left, right));
  };
  // Scenarios: written here, checked by the simulator, reserved from here;
  // `call sim` on the box then dials what was reserved.
  const loadScenarios = async () => {
    const [sc, rc] = await Promise.all([api('/api/scenarios'), api('/api/scenarios/calls').catch(() => ({ calls: [] }))]);
    state.scenarios = sc.scenarios || []; state.realCalls = rc.calls || []; state.scenariosAt = Date.now();
    if (!state.scenario && state.scenarios.length) state.scenario = state.scenarios[0].name;
  };
  const openScenario = async (name) => {
    const d = await api('/api/scenarios/' + encodeURIComponent(name));
    state.scenario = name; state.edit = d.scenario; state.editName = d.source === 'shipped' ? name + '-copy' : name; state.editSource = d.source;
    render();
  };
  const lines = (arr) => (arr || []).join('\n');
  const kv = (obj) => Object.entries(obj || {}).map(([k, v]) => k + ' = ' + v).join('\n');
  const renderScenarios = () => {
    const rows = state.scenarios;
    const left = h('div', {},
      h('div', { class: 'row' }, h('button', { class: 'btn', onclick: async () => { try { await loadScenarios(); render(); } catch (e) { tip(e.message, true); } } }, 'Refresh'),
        h('button', { class: 'btn btn-primary', onclick: () => { state.scenario = ''; state.editSource = 'custom'; state.editName = 'my-scenario';
          state.edit = { schema_version: 1, id: 'my-scenario', language: 'zh-CN', persona: '', facts: {}, greeting: '喂，你好，哪位？', answer_delay_ms: 1500, max_duration_ms: 290000, max_remote_turns: 40, idle_timeout_ms: 20000, actions: [], retain_transcript: true, retain_audio: false, goal: '', behavior: [], expectations: [], expected_outcome: 'conversation' }; render(); } }, 'New')),
      h('div', { class: 'threads' }, rows.length ? rows.map(x =>
        h('button', { 'aria-current': String(state.scenario === x.name), onclick: () => openScenario(x.name).catch(e => tip(e.message, true)) },
          h('span', { class: 't-subject' }, x.name, ' ', h('span', { class: 'chip' + (x.source === 'shipped' ? '' : ' good') }, x.source)),
          h('span', { class: 't-meta' }, (x.language || '?') + ' · ' + x.max_remote_turns + ' turns · ' + Math.round(x.max_duration_ms / 1000) + ' s' + (x.goal ? ' · ' + x.goal.slice(0, 70) : ''))))
        : h('div', { class: 'empty' }, 'no scenarios found under the simulator\u2019s directory')),
      h('div', { class: 'card', style: 'margin-top:12px' },
        h('h2', { style: 'margin-top:0' }, 'Draft from a real call'),
        h('p', { class: 'sub' }, 'The caller\u2019s side of a real call, read on the box, becomes a synthetic customer: persona, goal, how they behave over many turns, what to expect of the assistant. Every particular is replaced by an invented one. A draft until you save it.'),
        (() => {
          const sel = h('select', {}, ...state.realCalls.map(c => h('option', { value: c.id }, when(c.started_at) + ' · ' + c.turns + ' turns · ' + dur(c.duration_s))));
          const turns = h('input', { type: 'number', class: 'short', value: '40', min: '4', max: '100' });
          const btn = h('button', { class: 'btn btn-primary', onclick: async () => {
            if (!sel.value) return tip('no real call to draft from', true);
            btn.disabled = true; btn.textContent = 'asking the chat model…';
            try {
              const d = await api('/api/scenarios/draft', { method: 'POST', body: JSON.stringify({ call: sel.value, turns: Number(turns.value) || 40 }) });
              state.scenario = ''; state.edit = d.scenario; state.editName = d.scenario.id; state.editSource = 'custom'; render(); tip('drafted; read it, then Validate and Save');
            } catch (e) { tip(e.message, true); btn.disabled = false; btn.textContent = 'Draft'; }
          } }, 'Draft');
          return h('div', {}, h('label', {}, 'call'), sel, h('div', { class: 'row' }, 'turns ', turns, btn));
        })()));

    const right = h('div', {});
    const e = state.edit;
    if (e) {
      const f = {};
      const field = (key, label, el) => { f[key] = el; return [h('label', {}, label), el]; };
      const text = (key, label, val, attrs = {}) => field(key, label, h('input', { type: 'text', value: val == null ? '' : String(val), ...attrs }));
      const num = (key, label, val) => field(key, label, h('input', { type: 'number', class: 'short', value: String(val == null ? '' : val) }));
      const area = (key, label, val, rows = 4) => field(key, label, h('textarea', { rows: String(rows), style: 'white-space:pre-wrap;min-height:' + (rows * 22) + 'px' }, val || ''));
      const gather = () => {
        const facts = {};
        for (const line of f.facts.value.split('\n')) { const i = line.indexOf('='); if (i > 0) facts[line.slice(0, i).trim()] = line.slice(i + 1).trim(); }
        const expectations = f.expectations.value.split('\n').map(l => l.trim()).filter(Boolean).map(l => { const i = l.indexOf(':'); return i > 0 ? { id: l.slice(0, i).trim(), description: l.slice(i + 1).trim() } : { id: l.replace(/\W+/g, '_').toLowerCase().slice(0, 32), description: l }; });
        let actions = [];
        try { actions = JSON.parse(f.actions.value || '[]'); } catch (err) { throw new Error('actions is not JSON: ' + err.message); }
        const out = { schema_version: 1, id: f.id.value.trim(), language: f.language.value.trim(), persona: f.persona.value.trim(), facts, greeting: f.greeting.value,
          answer_delay_ms: Number(f.answer_delay_ms.value), max_duration_ms: Number(f.max_duration_ms.value), max_remote_turns: Number(f.max_remote_turns.value), idle_timeout_ms: Number(f.idle_timeout_ms.value),
          actions, retain_transcript: f.retain_transcript.checked, retain_audio: false, goal: f.goal.value.trim(), behavior: f.behavior.value.split('\n').map(l => l.trim()).filter(Boolean), expectations, expected_outcome: f.expected_outcome.value };
        if (!out.goal) delete out.goal; if (!out.behavior.length) delete out.behavior; if (!out.expectations.length) delete out.expectations;
        return out;
      };
      const nameIn = h('input', { type: 'text', value: state.editName, placeholder: 'file name: lower-case, digits, dashes' });
      const status = h('div', { class: 'muted', style: 'margin:8px 0' }, state.editSource === 'shipped' ? 'a shipped scenario: read-only, saved under another name' : '');
      const show = (r) => { status.textContent = typeof r === 'string' ? r : JSON.stringify(r); };
      right.append(h('div', { class: 'card' },
        h('h2', { style: 'margin-top:0' }, state.scenario ? 'Scenario ' + state.scenario : 'A new scenario'),
        e._drafted_from ? h('p', { class: 'sub' }, 'drafted from call ', h('span', { class: 'mono' }, e._drafted_from.call), ', ' + e._drafted_from.caller_turns + ' caller turns, ' + dur(e._drafted_from.duration_s)) : null,
        h('div', { class: 'grid2' },
          h('div', {}, ...text('id', 'id', e.id), ...text('language', 'language', e.language || 'zh-CN'), ...text('greeting', 'greeting (the customer\u2019s first words)', e.greeting)),
          h('div', {}, h('div', { class: 'row' }, ...num('answer_delay_ms', 'answer delay ms', e.answer_delay_ms), ...num('max_duration_ms', 'max duration ms (≤ 300000)', e.max_duration_ms)),
            h('div', { class: 'row' }, ...num('max_remote_turns', 'max assistant turns (≤ 100)', e.max_remote_turns), ...num('idle_timeout_ms', 'idle timeout ms', e.idle_timeout_ms)),
            h('div', { class: 'row' }, h('label', {}, 'keep transcript'), f.retain_transcript = h('input', { type: 'checkbox', checked: e.retain_transcript ? '' : null }),
              h('label', {}, 'expected outcome'), f.expected_outcome = h('select', {}, ...['conversation', 'cancel_before_answer'].map(v => h('option', { value: v, selected: e.expected_outcome === v ? '' : null }, v)))))),
        ...area('persona', 'persona', e.persona, 3),
        ...area('goal', 'goal', e.goal, 2),
        ...area('behavior', 'behavior, one per line, in the order of the conversation (up to 16)', lines(e.behavior), 8),
        ...area('facts', 'facts, key = value per line (all invented)', kv(e.facts), 4),
        ...area('expectations', 'expectations of the assistant, id: description per line (up to 16)', (e.expectations || []).map(x => x.id + ': ' + x.description).join('\n'), 5),
        ...area('actions', 'actions, JSON (scripted interruptions or a hangup; [] for none)', JSON.stringify(e.actions || [], null, 1), 3),
        h('label', {}, 'save as'), nameIn, status,
        h('div', { class: 'row' },
          h('button', { class: 'btn', onclick: async () => { try { const r = await api('/api/scenarios/validate', { method: 'POST', body: JSON.stringify(gather()) }); show(r.scenario ? 'valid: the simulator accepts it' : r); tip('valid'); } catch (err) { show(err.message); tip(err.message, true); } } }, 'Validate'),
          h('button', { class: 'btn btn-primary', onclick: async () => { try { const r = await api('/api/scenarios/' + encodeURIComponent(nameIn.value.trim()), { method: 'PUT', body: JSON.stringify(gather()) }); show('saved as ' + r.saved); await loadScenarios(); state.scenario = r.saved; state.editSource = 'custom'; state.editName = r.saved; render(); tip('saved'); } catch (err) { show(err.message); tip(err.message, true); } } }, 'Save'),
          state.scenario && state.editSource === 'custom' ? h('button', { class: 'btn', onclick: async () => { try { const r = await api('/api/scenarios/' + encodeURIComponent(state.scenario) + '/prepare', { method: 'POST', body: '{}' }); show('reserved run ' + r.run_id + ' → ' + r.dial_target_reference + ' (120 s): now `call sim` on the box'); tip('run reserved'); } catch (err) { show(err.message); tip(err.message, true); } } }, 'Prepare a run') : null)));
    } else {
      right.append(h('div', { class: 'empty' }, 'choose a scenario, draft one from a real call, or start a new one'));
    }
    $('#main').replaceChildren(h('div', { class: 'desk' }, left, right));
  };

  const journalURL = (call) => location.protocol + '//' + location.hostname + ':' + state.journalPort + '/#/call/' + encodeURIComponent(call);

  const render = () => {
    $('#nav-notes').setAttribute('aria-current', state.view === 'notes' ? 'page' : 'false');
    $('#nav-calls').setAttribute('aria-current', state.view === 'calls' ? 'page' : 'false');
    $('#nav-scenarios').setAttribute('aria-current', state.view === 'scenarios' ? 'page' : 'false');
    $('#who').replaceChildren(h('span', { class: 'me' }, state.me));
    $('#seen').replaceChildren(...['owner', 'claude', 'codex'].map(p =>
      h('span', {}, h('b', {}, p), ' ' + ago(state.seen[p]) + (state.open[p] ? ' · ' + state.open[p] + ' open' : ''))));
    const c = state.control || {};
    const ctl = $('#control');
    ctl.replaceChildren(
      h('span', { class: c.paused ? 'paused' : 'live' }, c.paused ? 'paused' : 'live'),
      h('span', { class: 'muted' }, 'mode ' + (c.mode || 'manual')),
      state.me === 'owner' ? h('button', { class: 'btn ' + (c.paused ? 'btn-primary' : 'btn-danger'), onclick: async () => {
        try { await api('/api/control', { method: 'PUT', body: JSON.stringify({ paused: !c.paused }) }); await refresh(); render(); tip(c.paused ? 'the agents may write again' : 'the agents can read but not write'); }
        catch (e) { tip(e.message, true); }
      } }, c.paused ? 'Resume' : 'Pause') : null,
      state.me === 'owner' ? h('select', { onchange: async (e) => {
        try { await api('/api/control', { method: 'PUT', body: JSON.stringify({ mode: e.target.value }) }); await refresh(); render(); tip('mode ' + e.target.value + (e.target.value === 'auto' ? ' — written down; no scheduler acts on it yet' : '')); }
        catch (err) { tip(err.message, true); }
      } }, ...['manual', 'auto'].map(m => h('option', { value: m, selected: c.mode === m ? '' : null }, m))) : null);

    if (state.view === 'scenarios') {
      if (!state.scenariosAt) loadScenarios().then(() => { if (state.scenario && !state.edit) openScenario(state.scenario).catch(() => render()); else render(); }).catch(e => tip(e.message, true));
      renderScenarios();
      return;
    }
    if (state.view === 'calls') {
      if (Date.now() - state.callsAt > 10000) loadCalls().then(renderCalls).catch(e => tip(e.message, true));
      renderCalls();
      return;
    }
    const ts = threads().filter(matches);
    if (!state.thread && ts.length) state.thread = ts[0].id;
    const left = h('div', {},
      h('div', { class: 'filters' }, ...[['all', 'All'], ['open', 'Open'], ['me', 'Mine'], ['owner', 'From owner'], ['claude', 'From Claude'], ['codex', 'From Codex']].map(([k, label]) =>
        h('button', { class: 'btn', 'aria-pressed': String(state.filter === k), onclick: () => { state.filter = k; state.thread = ''; render(); } }, label))),
      h('div', { class: 'threads' }, ts.length ? ts.map(t => {
        const first = t.notes[0], last = t.notes[t.notes.length - 1], open = t.notes.filter(n => n.open).length;
        return h('button', { 'aria-current': String(state.thread === t.id), onclick: () => { state.thread = t.id; render(); } },
          h('span', { class: 't-subject' }, first.subject || first.body.slice(0, 60)),
          h('span', { class: 't-meta' }, first.from + ' → ' + first.to + ' · ' + t.notes.length + (t.notes.length === 1 ? ' note' : ' notes') + (open ? ' · ' + open + ' open' : '') + ' · ' + ago(last.at)));
      }) : h('div', { class: 'empty' }, 'nothing yet')),
      h('div', { class: 'row', style: 'margin-top:10px' }, h('button', { class: 'btn btn-primary', onclick: () => { state.thread = 'new'; render(); } }, 'New thread')));

    const right = h('div', {});
    const t = ts.find(x => x.id === state.thread);
    if (state.thread === 'new') {
      right.append(h('h2', {}, 'A new thread'), compose(null));
    } else if (t) {
      right.append(...t.notes.map(noteCard), h('div', { class: 'compose' }, compose(t.notes[0])));
    } else {
      right.append(h('div', { class: 'empty' }, 'choose a thread or start one'));
    }
    $('#main').replaceChildren(h('div', { class: 'desk' }, left, right));
  };

  const refLink = (r) => /^https?:\/\//.test(r) ? h('a', { href: r, target: '_blank', rel: 'noopener' }, r) : r;
  const noteCard = (n) => h('div', { class: 'note from-' + n.from },
    h('div', { class: 'n-head' }, h('b', {}, n.from), '→ ' + n.to, h('span', { class: 'chip' + (n.open ? ' warn' : (n.kind === 'task' || n.kind === 'question') ? ' good' : '') }, n.kind + (n.open ? ' · open' : n.done_by ? ' · done by ' + n.done_by : '')),
      h('span', {}, when(n.at)), h('span', { class: 'mono' }, n.id),
      n.open && (state.me === 'owner' || n.to === state.me || n.to === 'all' || n.from === state.me) ? h('button', { class: 'btn btn-quiet', onclick: async () => {
        try { await api('/api/notes/' + n.id + '/done', { method: 'POST', body: '{}' }); await refresh(); render(); } catch (e) { tip(e.message, true); }
      } }, 'Mark done') : null),
    n.subject ? h('div', { class: 'n-subject' }, n.subject) : null,
    n.body ? h('div', { class: 'n-body' }, n.body) : null,
    n.refs && n.refs.length ? h('div', { class: 'n-refs' }, ...n.refs.flatMap((r, i) => [i ? ' · ' : 'refs: ', refLink(r)])) : null);

  const compose = (root) => {
    const others = ['all', 'owner', 'claude', 'codex'].filter(p => p !== state.me);
    const to = h('select', {}, ...others.map(p => h('option', { value: p, selected: root && root.from !== state.me && root.from === p ? '' : null }, p)));
    const kind = h('select', {}, ...['message', 'task', 'question', 'result', 'status'].map(k => h('option', { value: k }, k)));
    const subject = h('input', { type: 'text', placeholder: root ? 'subject (optional in a reply)' : 'subject' });
    const body = h('textarea', { placeholder: 'what you want to say. Numbers and shapes; no transcripts, no keys, no phone numbers.' });
    const refs = h('input', { type: 'text', placeholder: 'refs, comma-separated: a commit, a run id, a call id, a link' });
    const paused = state.control && state.control.paused && state.me !== 'owner';
    return h('div', { class: 'card' },
      h('div', { class: 'row' }, 'to ', to, ' kind ', kind),
      subject, h('label', {}, 'body'), body, h('label', {}, 'refs'), refs,
      h('div', { class: 'row' }, h('button', { class: 'btn btn-primary', disabled: paused ? '' : null, onclick: async () => {
        const note = { to: to.value, kind: kind.value, subject: subject.value, body: body.value, refs: refs.value.split(',').map(s => s.trim()).filter(Boolean) };
        if (root) note.reply_to = root.id;
        try {
          const v = await api('/api/notes', { method: 'POST', body: JSON.stringify(note) });
          if (!state.notes.some(x => x.id === v.id)) state.notes.push(v);
          state.thread = v.thread; render(); tip('sent as ' + v.id);
        } catch (e) { tip(e.message, true); }
      } }, root ? 'Reply' : 'Post'), paused ? h('span', { class: 'muted' }, 'the desk is paused; you can read') : null));
  };

  if (token) start(); else gate();
})();
