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
  const state = { me: '', control: {}, notes: [], seq: 0, seen: {}, open: {}, thread: '', filter: 'all', es: null };

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

  const render = () => {
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
