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
    scenarios: [], scenario: '', edit: null, editName: '', realCalls: [], scenariosAt: 0,
    logs: [], logsAt: 0, stream: '', lines: [], tail: null, follow: true, logFilter: '',
    talker: null, talkerAt: 0, tk: { context: [], caller: '', opener: '', auto: true, turns: 6, live: '', result: null, running: false, history: [], prompt: '', showPrompt: false, error: '', raw: [], request: null, showRaw: true } };

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
    state.view = p[0] === 'calls' ? 'calls' : p[0] === 'scenarios' ? 'scenarios' : p[0] === 'logs' ? 'logs' : p[0] === 'talker' ? 'talker' : 'notes';
    if (state.view === 'calls' && p[1]) state.call = decodeURIComponent(p[1]);
    if (state.view === 'scenarios' && p[1]) state.scenario = decodeURIComponent(p[1]);
    if (state.view === 'logs' && p[1] && p[1] !== state.stream) { state.stream = decodeURIComponent(p[1]); state.lines = []; if (state.tail) { state.tail.close(); state.tail = null; } }
    if (state.view !== 'logs' && state.tail) { state.tail.close(); state.tail = null; }
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
  let EFFECTS = {};
  const loadScenarios = async () => {
    const [sc, rc, ef] = await Promise.all([api('/api/scenarios'), api('/api/scenarios/calls').catch(() => ({ calls: [] })), api('/api/scenarios/effects').catch(() => ({ effects: {} }))]);
    state.scenarios = sc.scenarios || []; state.realCalls = rc.calls || []; EFFECTS = ef.effects || {}; state.scenariosAt = Date.now();
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
          const effect = h('select', {}, h('option', { value: '' }, 'plain'), ...Object.entries(EFFECTS).map(([k, v]) => h('option', { value: k, title: v }, k.replace('_', ' '))));
          const btn = h('button', { class: 'btn btn-primary', onclick: async () => {
            if (!sel.value) return tip('no real call to draft from', true);
            btn.disabled = true; btn.textContent = 'asking the chat model…';
            try {
              const d = await api('/api/scenarios/draft', { method: 'POST', body: JSON.stringify({ call: sel.value, turns: Number(turns.value) || 40, effect: effect.value }) });
              state.scenario = ''; state.edit = d.scenario; state.editName = d.scenario.id; state.editSource = 'custom'; render(); tip('drafted; read it, then Validate and Save');
            } catch (e) { tip(e.message, true); btn.disabled = false; btn.textContent = 'Draft'; }
          } }, 'Draft');
          return h('div', {}, h('label', {}, 'call'), sel, h('div', { class: 'row' }, 'turns ', turns, 'effect ', effect, btn));
        })()),
      h('div', { class: 'card', style: 'margin-top:12px' },
        h('h2', { style: 'margin-top:0' }, 'Synthesize a set'),
        h('p', { class: 'sub' }, 'Several real calls at once, each drafted and saved plain and with each effect ticked — the practical things callers do: cutting in, listening sounds over the answer, being short of time, hanging up — and a suite manifest naming them all. Each scenario is checked by the simulator. Takes a minute per call.'),
        (() => {
          const boxes = state.realCalls.slice(0, 20).map(c => { const cb = h('input', { type: 'checkbox', value: c.id }); return { cb, el: h('label', { style: 'display:flex;gap:6px;align-items:center;margin:2px 0' }, cb, when(c.started_at) + ' · ' + c.turns + ' turns · ' + dur(c.duration_s)) }; });
          const effs = Object.entries(EFFECTS).map(([k, v]) => { const cb = h('input', { type: 'checkbox', value: k, checked: (k === 'barge_in' || k === 'backchannel') ? '' : null }); return { cb, el: h('label', { style: 'display:flex;gap:6px;align-items:center;margin:2px 0', title: v }, cb, k.replace('_', ' ') + ' — ' + v) }; });
          const prefix = h('input', { type: 'text', value: 'set-' + new Date().toISOString().slice(5, 10).replace('-', ''), placeholder: 'prefix' });
          const turns = h('input', { type: 'number', class: 'short', value: '40', min: '4', max: '100' });
          const repeat = h('input', { type: 'number', class: 'short', value: '1', min: '1', max: '10' });
          const out = h('div', { class: 'muted', style: 'margin-top:6px;white-space:pre-wrap' });
          const btn = h('button', { class: 'btn btn-primary', onclick: async () => {
            const calls = boxes.filter(b => b.cb.checked).map(b => b.cb.value);
            if (!calls.length) return tip('tick at least one call', true);
            btn.disabled = true; btn.textContent = 'synthesizing…';
            try {
              const r = await api('/api/scenarios/synthesize', { method: 'POST', body: JSON.stringify({ calls, effects: effs.filter(e => e.cb.checked).map(e => e.cb.value), turns: Number(turns.value) || 40, prefix: prefix.value.trim(), repeat: Number(repeat.value) || 1 }) });
              out.textContent = r.count + ' scenarios saved' + (r.suite ? ', suite ' + r.suite : '') + '\n' + r.made.map(m => (m.name || m.call) + (m.error ? ' — ' + m.error : '')).join('\n');
              await loadScenarios(); render(); tip(r.count + ' scenarios saved');
            } catch (e) { tip(e.message, true); }
            btn.disabled = false; btn.textContent = 'Synthesize';
          } }, 'Synthesize');
          return h('div', {}, h('label', {}, 'calls'), h('div', { style: 'max-height:180px;overflow:auto' }, ...boxes.map(b => b.el)), h('label', {}, 'effects'), ...effs.map(e => e.el),
            h('div', { class: 'row' }, 'prefix ', prefix, 'turns ', turns, 'repeat ', repeat, btn), out);
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
        e._drafted_from ? h('p', { class: 'sub' }, 'drafted from call ', h('span', { class: 'mono' }, e._drafted_from.call), ', ' + e._drafted_from.caller_turns + ' caller turns, ' + dur(e._drafted_from.duration_s), e._effect ? ' · effect ' + e._effect.replace('_', ' ') : '') : null,
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

  // ----- the work log: what an agent is doing, as it does it -----
  const loadLogs = async () => {
    const d = await api('/api/logs');
    state.logs = d.streams || []; state.logsAt = Date.now();
    if (!state.stream && state.logs.length) state.stream = state.logs[0].name;
  };
  const tailLog = () => {
    if (state.tail || !state.stream) return;
    const stream = state.stream;
    const ctl = new AbortController();
    state.tail = { close: () => ctl.abort(), stream };
    (async () => {
      try {
        const after = state.lines.length ? state.lines[state.lines.length - 1].n : 0;
        const r = await fetch('/api/logs/' + encodeURIComponent(stream) + '/tail?after=' + after, { headers: { 'Authorization': 'Bearer ' + token }, signal: ctl.signal });
        const reader = r.body.getReader(); const dec = new TextDecoder(); let buf = '';
        for (;;) {
          const { value, done } = await reader.read(); if (done) break;
          buf += dec.decode(value, { stream: true });
          let i; let changed = false;
          while ((i = buf.indexOf('\n\n')) >= 0) {
            const chunk = buf.slice(0, i); buf = buf.slice(i + 2);
            let ev = '', data = '';
            for (const line of chunk.split('\n')) { if (line.startsWith('event: ')) ev = line.slice(7); else if (line.startsWith('data: ')) data += line.slice(6); }
            if (ev === 'line') { const l = JSON.parse(data); if (!state.lines.length || l.n > state.lines[state.lines.length - 1].n) { state.lines.push(l); changed = true; } }
          }
          if (changed && state.view === 'logs' && state.stream === stream) renderLogLines();
        }
      } catch (e) { if (!ctl.signal.aborted) { state.tail = null; setTimeout(() => { if (state.view === 'logs' && state.stream === stream) tailLog(); }, 3000); } }
    })();
  };
  const fmtTime = (iso) => { const d = new Date(iso); return isNaN(d) ? '' : d.toLocaleTimeString(undefined, { hour12: false }); };
  const renderLogLines = () => {
    const box = $('#loglines'); if (!box) return;
    const q = state.logFilter.trim();
    const lines = q ? state.lines.filter(l => l.text.includes(q)) : state.lines;
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
    // A line that starts with "note:" (or 说明：) is the agent's own words —
    // what it decided and why — and is set apart from command output.
    box.replaceChildren(...lines.slice(-3000).map(l => h('div', { class: 'logline' + (/^(note|说明|reply|回复)[:：]/i.test(l.text) ? ' note' : /error|fail|traceback|refused|exception/i.test(l.text) ? ' bad' : /host=|device=|model=/i.test(l.text) ? ' mark' : '') },
      h('span', { class: 'ts' }, fmtTime(l.at)), h('span', { class: 'who' }, l.who), h('span', { class: 'txt' }, l.text))));
    const meta = $('#logmeta'); if (meta) meta.textContent = state.lines.length + ' lines' + (state.lines.length ? ' · last ' + ago(state.lines[state.lines.length - 1].at) : '');
    renderProgress();
    if (state.follow || atBottom) box.scrollTop = box.scrollHeight;
  };
  // Progress, from the stream's own lines: "stage: <name> started|done|failed",
  // "progress: <stage> <k>/<n>" (or <pct>%), "metric: k=v k=v …" (numbers,
  // plotted over time). The agent prints them; the page draws them.
  const progressOf = (lines) => {
    const stages = []; const byName = {}; const metrics = {}; let host = '';
    const stage = (name) => { if (!byName[name]) { byName[name] = { name, status: 'pending', done: 0, total: 0 }; stages.push(byName[name]); } return byName[name]; };
    for (const l of lines) {
      const t = l.text;
      let m;
      if (!host && /host=/.test(t)) host = t;
      if ((m = /^stage[:：]\s*(\S+)\s+(started|running|done|ok|failed|skipped)/i.exec(t))) {
        const st = stage(m[1]); const w = m[2].toLowerCase();
        st.status = w === 'started' || w === 'running' ? 'running' : w === 'done' || w === 'ok' ? 'done' : w; st.at = l.at;
        if (st.status === 'done') { st.done = st.total || 1; st.total = st.total || 1; }
      } else if ((m = /^progress[:：]\s*(\S+)\s+(\d+)\s*\/\s*(\d+)/i.exec(t))) {
        const st = stage(m[1]); st.done = +m[2]; st.total = +m[3]; st.at = l.at; if (st.status === 'pending') st.status = 'running'; if (st.done >= st.total && st.total > 0) st.status = 'done';
      } else if ((m = /^progress[:：]\s*(\S+)\s+(\d+(?:\.\d+)?)\s*%/i.exec(t))) {
        const st = stage(m[1]); st.done = +m[2]; st.total = 100; st.at = l.at; if (st.status === 'pending') st.status = 'running'; if (st.done >= 100) st.status = 'done';
      } else if ((m = /^metric[:：]\s*(.+)$/i.exec(t))) {
        for (const kv of m[1].matchAll(/([A-Za-z_][\w./-]*)\s*=\s*(-?\d+(?:\.\d+)?(?:e-?\d+)?)/g)) {
          (metrics[kv[1]] = metrics[kv[1]] || []).push({ n: l.n, at: l.at, v: +kv[2] });
        }
      }
    }
    return { stages, metrics, host };
  };
  const sparkline = (name, pts) => {
    const W = 260, H = 60, P = 6;
    const vs = pts.map(p => p.v); const lo = Math.min(...vs), hi = Math.max(...vs);
    const x = (i) => P + (pts.length > 1 ? i * (W - 2 * P) / (pts.length - 1) : (W - 2 * P) / 2);
    const y = (v) => hi === lo ? H / 2 : H - P - (v - lo) * (H - 2 * P) / (hi - lo);
    const d = pts.map((p, i) => (i ? 'L' : 'M') + x(i).toFixed(1) + ' ' + y(p.v).toFixed(1)).join(' ');
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H); svg.setAttribute('class', 'spark');
    svg.innerHTML = '<path d="' + d + '" fill="none" stroke="currentColor" stroke-width="1.5"/>' +
      '<circle cx="' + x(pts.length - 1).toFixed(1) + '" cy="' + y(vs[vs.length - 1]).toFixed(1) + '" r="2.5" fill="currentColor"/>';
    const last = vs[vs.length - 1];
    return h('div', { class: 'metric' }, h('div', { class: 'row' }, h('b', {}, name), h('span', { class: 'muted small' }, 'last ' + last + ' · min ' + lo + ' · max ' + hi + ' · ' + pts.length + ' points')), svg);
  };
  const renderProgress = () => {
    const box = $('#logprogress'); if (!box) return;
    const p = progressOf(state.lines);
    if (!p.stages.length && !Object.keys(p.metrics).length) { box.replaceChildren(h('p', { class: 'muted small' }, 'No progress marks yet. The agent prints "stage: <name> started|done", "progress: <stage> k/n" and "metric: loss=… step=…" lines; they are drawn here as they arrive.')); return; }
    const chips = h('div', { class: 'stages' }, ...p.stages.map(st => {
      const pct = st.total ? Math.min(100, Math.round(100 * st.done / st.total)) : (st.status === 'done' ? 100 : 0);
      return h('div', { class: 'stage ' + st.status },
        h('div', { class: 'row' }, h('span', { class: 'dot' }), h('b', {}, st.name), h('span', { class: 'muted small' }, st.status + (st.total ? ' · ' + st.done + '/' + st.total : '') + (st.at ? ' · ' + ago(st.at) : ''))),
        h('div', { class: 'bar' }, h('div', { class: 'fill', style: 'width:' + pct + '%' })));
    }));
    const charts = h('div', { class: 'metrics' }, ...Object.entries(p.metrics).map(([k, pts]) => sparkline(k, pts)));
    box.replaceChildren(chips, charts);
  };
  const renderLogs = () => {
    const left = h('div', {},
      h('div', { class: 'row' }, h('button', { class: 'btn', onclick: async () => { try { await loadLogs(); render(); } catch (e) { tip(e.message, true); } } }, 'Refresh'),
        h('span', { class: 'muted' }, state.logs.length + (state.logs.length === 1 ? ' stream' : ' streams'))),
      h('div', { class: 'list' }, ...state.logs.map(x =>
        h('button', { 'aria-current': String(state.stream === x.name), onclick: () => { location.hash = '#/logs/' + encodeURIComponent(x.name); } },
          h('div', {}, h('b', {}, x.name), ' ', h('span', { class: 'muted' }, x.who)),
          h('div', { class: 'muted small' }, x.lines + ' lines · ' + ago(x.last_at)),
          x.first ? h('div', { class: 'muted small mono' }, x.first.slice(0, 90)) : null))),
      h('p', { class: 'muted small' }, 'An agent writes here as it works: scripts/desk.sh log <stream> "line", or cmd 2>&1 | scripts/desk.sh log <stream> -. Lines starting with "note:" are the agent\'s own words (what it decided, why, what it answered); "reply:" lines are its answers to Claude. The first line of a stream says where the work runs (host=… device=…). Long digit runs are masked before storage.'));
    const s = state.logs.find(x => x.name === state.stream);
    const right = h('div', { class: 'logpane' },
      h('div', { class: 'row' },
        h('h2', { class: 'inline' }, state.stream || 'no stream'),
        s && s.first ? h('span', { class: 'chip mono' }, s.first.slice(0, 120)) : null,
        h('span', { class: 'muted', id: 'logmeta' }, ''),
        h('input', { type: 'text', placeholder: 'filter', value: state.logFilter, class: 'short', oninput: (e) => { state.logFilter = e.target.value; renderLogLines(); } }),
        h('label', { class: 'row' }, h('input', { type: 'checkbox', checked: state.follow ? '' : null, onchange: (e) => { state.follow = e.target.checked; renderLogLines(); } }), ' follow')),
      h('div', { id: 'logprogress', class: 'progress' }),
      h('div', { id: 'loglines', class: 'loglines', onscroll: (e) => { const b = e.target; state.follow = b.scrollHeight - b.scrollTop - b.clientHeight < 40; } }));
    $('#main').replaceChildren(h('div', { class: 'grid' }, left, right));
    renderLogLines();
    tailLog();
  };

  /* The talker: Codex's local model on the Mac, tried from here. The desk
     renders the training's prompt from the conversation below, the caller's
     words and the opener the engine would have said at the pause, streams
     the model's first clause as it comes and times it. */
  const PAUSE_MS = 380;
  const loadTalker = async () => { state.talker = await api('/api/talker'); state.talkerAt = Date.now(); };
  const matchOpener = (text) => {
    const t = (text || '').trim(); if (!t || !state.talker) return '';
    const q = /[?？]$|吗|呢|多少|怎么|什么|哪|几|能不能|要不要|是不是/.test(t);
    for (const sc of state.talker.openers || []) {
      if (sc.enabled === false) continue;
      if (sc.kind === 'question' && !q) continue;
      if (sc.kind === 'statement' && q) continue;
      if ((sc.keywords || []).some(k => t.includes(k))) return { scenario: sc, phrase: sc.phrases[0] };
    }
    return { scenario: null, phrase: '嗯，' };
  };
  const openerNow = () => state.tk.auto ? matchOpener(state.tk.caller).phrase : state.tk.opener;
  const talkerAsk = async () => {
    const tk = state.tk;
    if (tk.running || !tk.caller.trim()) return;
    tk.running = true; tk.live = ''; tk.result = null; tk.error = ''; tk.raw = []; tk.request = null; renderTalker();
    const body = { context: tk.context, caller: tk.caller, opener: openerNow(), max_context_turns: tk.turns };
    try {
      const r = await fetch('/api/talker', { method: 'POST', headers: { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      if (!r.ok) { const e = await r.json().catch(() => ({})); throw new Error(e.error || ('HTTP ' + r.status)); }
      const reader = r.body.getReader(); const dec = new TextDecoder(); let buf = '';
      for (;;) {
        const { value, done } = await reader.read(); if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const chunk = buf.slice(0, i); buf = buf.slice(i + 2);
          let ev = '', data = '';
          for (const line of chunk.split('\n')) { if (line.startsWith('event: ')) ev = line.slice(7); else if (line.startsWith('data: ')) data += line.slice(6); }
          if (ev === 'token') { tk.live += JSON.parse(data).text; renderTalkerLive(); }
          else if (ev === 'raw') { tk.raw.push(JSON.parse(data)); renderTalkerRaw(); }
          else if (ev === 'request') { tk.request = JSON.parse(data); renderTalkerRaw(); }
          else if (ev === 'done') { tk.result = JSON.parse(data); tk.result.opener = body.opener; tk.result.at = Date.now(); tk.history.unshift(tk.result); if (tk.history.length > 40) tk.history.length = 40; }
          else if (ev === 'error') { tk.error = JSON.parse(data).error; }
        }
      }
    } catch (e) { tk.error = e.message; }
    tk.running = false; renderTalker();
  };
  const verdictWord = (v) => ({ ok: 'a clause the engine would say', ack: 'a bare acknowledgement — the opener already was one', long: 'over the 12-character cap', incomplete: 'no clause boundary before the model stopped', empty: 'nothing', special: 'a template token leaked' })[v] || v;
  const pct = (ms) => Math.max(0, Math.min(100, 100 * ms / 1200));
  const timeBar = (r) => {
    // The figures, then one bar 0–1200 ms from the request: the first token
    // and the clause as filled segments, the 380 ms pause the opener covers
    // and the 1000 goal as ticks.
    const stat = (label, ms, cls) => h('div', { class: 'stat ' + (cls || '') }, h('div', { class: 'v num' }, ms ? Math.round(ms) : '—'), h('div', { class: 'k' }, label));
    return h('div', {},
      h('div', { class: 'stats' }, stat('first token', r.ttft_ms, 'ttft'), stat('complete clause', r.clause_ms, 'clause'), stat('done', r.total_ms), stat('prompt eval', r.prompt_ms), stat('predict', r.predict_ms)),
      h('div', { class: 'tbar' },
        h('div', { class: 'seg total', style: 'width:' + pct(r.total_ms) + '%' }),
        r.clause_ms ? h('div', { class: 'seg clause', style: 'width:' + pct(r.clause_ms) + '%' }) : null,
        h('div', { class: 'seg ttft', style: 'width:' + pct(r.ttft_ms) + '%' }),
        h('span', { class: 'mark pause', style: 'left:' + pct(PAUSE_MS) + '%' }, 'pause ends ' + PAUSE_MS),
        h('span', { class: 'mark goal', style: 'left:' + pct(1000) + '%' }, 'goal 1000'),
        h('span', { class: 'mark end', style: 'left:100%' }, '1200 ms')));
  };
  // The API's own output: the request the desk sent and every line
  // llama-server answered, with the desk's clock beside each.
  const rawText = () => {
    const tk = state.tk; const out = [];
    if (tk.request) out.push('POST ' + tk.request.url, JSON.stringify(tk.request.body, null, 1).replace(/\n\s*/g, ' '), '');
    for (const r of tk.raw) out.push(String(Math.round(r.ms)).padStart(5) + ' ms  ' + r.line);
    return out.join('\n') || (tk.running ? 'waiting for the first line…' : 'nothing yet — ask the talker');
  };
  const renderTalkerRaw = () => { const el = $('#tk-raw'); if (el) { el.textContent = rawText(); el.scrollTop = el.scrollHeight; } };
  const renderTalkerLive = () => {
    const el = $('#tk-live'); if (!el) return;
    el.textContent = state.tk.live || '…';
  };
  const renderTalker = () => {
    const t = state.talker, tk = state.tk;
    if (!t) { $('#main').replaceChildren(h('div', { class: 'card' }, h('p', { class: 'muted' }, 'asking the desk about the talker…'))); return; }
    if (!t.enabled) { $('#main').replaceChildren(h('div', { class: 'card' }, h('h1', {}, 'The talker'), h('p', { class: 'sub' }, 'No talker is configured on this desk: start it with -talker http://<mac>:18081 (TALKER_URL in phone-brain.sh).'))); return; }
    const m = tk.auto ? matchOpener(tk.caller) : null;
    const status = h('div', { class: 'row' },
      h('span', { class: 'chip ' + (t.up ? 'good' : 'bad') }, t.up ? 'up' : 'not reachable'),
      h('span', { class: 'mono muted' }, t.url + (t.model ? ' · ' + t.model : '') + (t.up ? ' · probe ' + t.probe_ms + ' ms' : '')),
      h('button', { class: 'btn btn-quiet', onclick: async () => { try { await loadTalker(); renderTalker(); } catch (e) { tip(e.message, true); } } }, 'Recheck'));
    const conv = h('div', { class: 'conv' }, tk.context.length ? tk.context.map((msg, i) =>
      h('div', { class: 'turn ' + msg.role },
        h('span', { class: 'role' }, msg.role === 'user' ? '来电者' : '助手'),
        h('span', { class: 'words' }, msg.content),
        h('button', { class: 'btn btn-quiet small', title: 'remove this turn', onclick: () => { tk.context.splice(i, 1); renderTalker(); } }, '×'))) :
      h('p', { class: 'muted small' }, 'No earlier turns. The model sees up to ' + tk.turns + ' prior exchanges; add them here, or let each answer you accept become one.'));
    const callerIn = h('textarea', { class: 'words-in', placeholder: '来电者刚说的话，比如：三者险一百万多少钱？', oninput: (e) => { tk.caller = e.target.value; if (tk.auto) { const mm = matchOpener(tk.caller); $('#tk-opener').value = mm.phrase || ''; $('#tk-scn').textContent = mm.scenario ? mm.scenario.name + ' (' + mm.scenario.id + ')' : 'no scenario → 嗯，'; } },
      onkeydown: (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); talkerAsk(); } } }, tk.caller);
    const openerIn = h('input', { type: 'text', id: 'tk-opener', value: openerNow(), disabled: tk.auto ? '' : null, oninput: (e) => { tk.opener = e.target.value; } });
    const openerPick = h('select', { onchange: (e) => { if (e.target.value) { tk.auto = false; tk.opener = e.target.value; renderTalker(); } } },
      h('option', { value: '' }, 'pick…'), ...(t.openers || []).flatMap(sc => sc.phrases.map(p => h('option', { value: p }, p + '  — ' + sc.name))), h('option', { value: '嗯，' }, '嗯，  — no scenario'));
    const ask = h('button', { class: 'btn btn-primary', disabled: tk.running || !t.up ? '' : null, onclick: talkerAsk }, tk.running ? 'asking…' : 'Ask the talker');
    const r = tk.result;
    const result = h('div', { class: 'card tk-result' },
      h('div', { class: 'spoken' },
        h('span', { class: 'opener' }, r ? r.opener : openerNow()),
        h('span', { class: 'clause', id: 'tk-live' }, r ? (r.clause || r.text || '') : (tk.running ? '…' : '')),
        r && r.clause && r.text.trim() !== r.clause ? h('span', { class: 'rest muted' }, ' ' + r.text.trim().slice(r.clause.length)) : null),
      tk.error ? h('div', { class: 'msg err' }, tk.error) : null,
      r ? h('div', {},
        h('div', { class: 'row' },
          h('span', { class: 'chip ' + (r.verdict === 'ok' ? 'good' : r.verdict === 'incomplete' || r.verdict === 'long' ? 'warn' : 'bad') }, r.verdict),
          h('span', { class: 'muted' }, verdictWord(r.verdict)),
          h('span', { class: 'muted num' }, r.tokens + ' tokens · prompt ' + r.prompt_tokens + ' (' + r.cached_tokens + ' cached) · ' + r.prompt_chars + ' chars')),
        timeBar(r),
        h('p', { class: 'muted small' }, 'Times are from the desk\'s request to the Mac. On a call the opener covers the 380 ms pause; a clause back before it ends would be said right behind the opener, where the chat model\'s first token (p95 400–650 from the close) is waited for today.'),
        h('div', { class: 'row' },
          h('button', { class: 'btn', disabled: r.verdict !== 'ok' && r.verdict !== 'long' ? '' : null, onclick: () => {
            tk.context.push({ role: 'user', content: tk.caller }, { role: 'assistant', content: (r.opener + r.clause).trim() });
            tk.caller = ''; tk.result = null; tk.live = ''; renderTalker();
          } }, 'Accept as the turn and continue'),
          h('button', { class: 'btn', onclick: () => { tk.result = null; tk.live = ''; renderTalker(); } }, 'Clear'))) : null);
    const promptBox = h('div', { class: 'card' },
      h('div', { class: 'row' }, h('h2', { class: 'inline' }, 'The prompt'), h('button', { class: 'btn btn-quiet', onclick: async () => {
        tk.showPrompt = !tk.showPrompt;
        if (tk.showPrompt) { try { const v = await api('/api/talker/prompt', { method: 'POST', body: JSON.stringify({ context: tk.context, caller: tk.caller || '（来电者的话）', opener: openerNow(), max_context_turns: tk.turns }) }); tk.prompt = v.prompt + '\n\n— ' + v.chars + ' characters'; } catch (e) { tk.prompt = e.message; } }
        renderTalker();
      } }, tk.showPrompt ? 'hide' : 'show')),
      tk.showPrompt ? h('pre', { class: 'pre' }, tk.prompt) : h('p', { class: 'muted small' }, 'Qwen ChatML as the training rendered it: the system line, up to ' + tk.turns + ' prior exchanges (oldest dropped to fit ' + 768 + ' characters), the caller\'s words, a no-thinking prefill, then the opener already said. The model continues from the opener.'));
    const rawBox = h('div', { class: 'card' },
      h('div', { class: 'row' }, h('h2', { class: 'inline' }, 'Raw API output'), h('span', { class: 'muted small' }, 'llama-server /completion, server-sent events as received; the ms column is the desk\'s clock from the request'),
        h('button', { class: 'btn btn-quiet', onclick: () => { tk.showRaw = !tk.showRaw; renderTalker(); } }, tk.showRaw ? 'hide' : 'show')),
      tk.showRaw ? h('pre', { class: 'pre raw', id: 'tk-raw' }, rawText()) : null);
    const hist = h('div', { class: 'card' },
      h('div', { class: 'row' }, h('h2', { class: 'inline' }, 'This session'), h('span', { class: 'muted' }, tk.history.length + (tk.history.length === 1 ? ' attempt' : ' attempts')),
        tk.history.length ? h('span', { class: 'muted num' }, 'first token p50 ' + q(tk.history.map(x => x.ttft_ms), 0.5) + ' · p95 ' + q(tk.history.map(x => x.ttft_ms), 0.95) + ' · clause p50 ' + q(tk.history.filter(x => x.clause_ms).map(x => x.clause_ms), 0.5) + ' · ok ' + tk.history.filter(x => x.verdict === 'ok').length + '/' + tk.history.length) : null),
      tk.history.length ? h('table', {}, h('thead', {}, h('tr', {}, h('th', {}, 'opener + clause'), h('th', {}, 'verdict'), h('th', { class: 'n' }, 'first token'), h('th', { class: 'n' }, 'clause'), h('th', { class: 'n' }, 'total'), h('th', { class: 'n' }, 'cached'))),
        h('tbody', {}, ...tk.history.map(x => h('tr', {}, h('td', {}, h('span', { class: 'opener-sm' }, x.opener), x.clause || h('span', { class: 'muted' }, x.text.slice(0, 30))), h('td', {}, h('span', { class: 'chip ' + (x.verdict === 'ok' ? 'good' : 'warn') }, x.verdict)),
          h('td', { class: 'n' }, Math.round(x.ttft_ms)), h('td', { class: 'n' }, x.clause_ms ? Math.round(x.clause_ms) : '—'), h('td', { class: 'n' }, Math.round(x.total_ms)), h('td', { class: 'n' }, x.cached_tokens + '/' + (x.prompt_tokens + x.cached_tokens)))))) :
        h('p', { class: 'muted small' }, 'Each attempt\'s numbers also go to the work log stream talker-web, where they chart; the words stay here.'));
    const left = h('div', {},
      h('div', { class: 'card' },
        h('h2', { style: 'margin-top:0' }, 'The conversation so far'), conv,
        h('div', { class: 'row' }, h('button', { class: 'btn btn-quiet', onclick: () => { tk.context = []; renderTalker(); } }, 'Start over'),
          h('label', { class: 'row', style: 'margin:0' }, 'turns kept ', h('input', { type: 'number', class: 'short', min: 0, max: 12, value: tk.turns, style: 'width:60px', oninput: (e) => { tk.turns = Math.max(0, Math.min(12, parseInt(e.target.value) || 0)); } }))),
        h('h2', {}, 'The caller says'), callerIn,
        h('h2', {}, 'The opener the engine said at the pause'),
        h('div', { class: 'row' }, openerIn, openerPick,
          h('label', { class: 'row', style: 'margin:0' }, h('input', { type: 'checkbox', checked: tk.auto ? '' : null, onchange: (e) => { tk.auto = e.target.checked; renderTalker(); } }), ' by the caller\'s words')),
        h('p', { class: 'muted small', id: 'tk-scn' }, tk.auto ? (m && m.scenario ? m.scenario.name + ' (' + m.scenario.id + ')' : 'no scenario → 嗯，') : 'chosen by hand'),
        h('div', { class: 'row' }, ask, h('span', { class: 'muted small' }, 'Enter asks; Shift+Enter is a newline'))));
    $('#main').replaceChildren(h('div', { class: 'tk-head' }, h('h1', {}, 'The talker'), h('p', { class: 'sub' }, 'Codex\'s Qwen3-0.6B talker, trained and served on the Mac: given the conversation, the caller\'s words and the opener already said at the pause, it writes the answer\'s first short clause. Try it as the engine would use it.'), status),
      h('div', { class: 'grid tk' }, left, h('div', {}, result, rawBox, promptBox, hist)));
  };
  const q = (vs, p) => { if (!vs.length) return '—'; const s = [...vs].sort((a, b) => a - b); return Math.round(s[Math.min(s.length - 1, Math.floor(p * (s.length - 1) + 0.5))]); };

  const render = () => {
    $('#nav-notes').setAttribute('aria-current', state.view === 'notes' ? 'page' : 'false');
    $('#nav-logs').setAttribute('aria-current', state.view === 'logs' ? 'page' : 'false');
    $('#nav-calls').setAttribute('aria-current', state.view === 'calls' ? 'page' : 'false');
    $('#nav-scenarios').setAttribute('aria-current', state.view === 'scenarios' ? 'page' : 'false');
    $('#nav-talker').setAttribute('aria-current', state.view === 'talker' ? 'page' : 'false');
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

    if (state.view === 'talker') {
      if (Date.now() - state.talkerAt > 20000) loadTalker().then(renderTalker).catch(e => tip(e.message, true));
      renderTalker();
      return;
    }
    if (state.view === 'logs') {
      if (Date.now() - state.logsAt > 15000) loadLogs().then(renderLogs).catch(e => tip(e.message, true));
      renderLogs();
      return;
    }
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
