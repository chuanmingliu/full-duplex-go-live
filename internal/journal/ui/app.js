/* Call journal: the page. Vanilla, no build step, no external resources.
   Every string from the API reaches the DOM through textContent. */
(function () {
  'use strict';

  // ---------- small DOM helpers ----------

  var SVGNS = 'http://www.w3.org/2000/svg';

  function setAttrs(el, attrs) {
    if (!attrs) return;
    Object.keys(attrs).forEach(function (k) {
      var v = attrs[k];
      if (v === null || v === undefined || v === false) return;
      if (k === 'class') el.setAttribute('class', v);
      else if (k === 'text') el.textContent = v;
      else if (k === 'style' && typeof v === 'object') {
        Object.keys(v).forEach(function (p) { el.style.setProperty(p, v[p]); });
      } else if (k.slice(0, 2) === 'on' && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? '' : String(v));
    });
  }
  function addKids(el, kids) {
    kids.forEach(function (c) {
      if (c === null || c === undefined || c === false) return;
      if (Array.isArray(c)) addKids(el, c);
      else if (c.nodeType) el.appendChild(c);
      else el.appendChild(document.createTextNode(String(c)));
    });
  }
  function h(tag, attrs) {
    var el = document.createElement(tag);
    setAttrs(el, attrs);
    addKids(el, Array.prototype.slice.call(arguments, 2));
    return el;
  }
  function s(tag, attrs) {
    var el = document.createElementNS(SVGNS, tag);
    setAttrs(el, attrs);
    addKids(el, Array.prototype.slice.call(arguments, 2));
    return el;
  }
  function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); }
  function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }

  // ---------- formatting ----------

  function pad(n) { return n < 10 ? '0' + n : '' + n; }
  function num(n) { // milliseconds and counts: no grouping below 10,000, as the figures are quoted everywhere else
    if (typeof n !== 'number' || !isFinite(n)) return '–';
    n = Math.round(n);
    return Math.abs(n) < 10000 ? String(n) : n.toLocaleString('en-US');
  }
  function when(iso) {
    var d = new Date(iso);
    if (isNaN(d)) return '–';
    return pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
  }
  function clock(d) {
    d = d instanceof Date ? d : new Date(d);
    if (isNaN(d)) return '–';
    return pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }
  function dur(sec) {
    if (!(sec >= 0)) return '–';
    sec = Math.round(sec);
    return Math.floor(sec / 60) + ':' + pad(sec % 60);
  }
  function pos(sec) { // a position in a recording, to the tenth
    if (!(sec >= 0)) return '–';
    var m = Math.floor(sec / 60), r = sec - m * 60;
    return m + ':' + (r < 10 ? '0' : '') + r.toFixed(1);
  }
  function pct(x) { return (Math.round(x * 10) / 10).toString(); }
  function plural(n, one, many) { return n + ' ' + (n === 1 ? one : (many || one + 's')); }
  function size(bytes) { // file sizes: KB below a megabyte, then MB, then GB
    if (!(bytes > 0)) return '0 KB';
    if (bytes < 1e6) return Math.max(1, Math.round(bytes / 1e3)) + ' KB';
    if (bytes < 1e9) return (bytes / 1e6).toFixed(bytes < 1e7 ? 1 : 0) + ' MB';
    return (bytes / 1e9).toFixed(1) + ' GB';
  }
  function archiveLine(a) { // the overview's sentence about the archive
    if (!a) return null;
    var mins = a.audio_s / 60;
    var audio = a.with_audio ? plural(a.with_audio, 'recording') + ' (' + (mins < 100 ? mins.toFixed(1) + ' minutes' : (mins / 60).toFixed(1) + ' hours') + ')' : 'no recordings yet';
    return h('p', { class: 'archive-line' },
      h('span', { class: 'archive-tag' }, 'Archive'),
      ' ' + plural(a.calls, 'call') + ' kept for good: conversation and measurements for every one, ' + audio + '. ',
      h('span', { class: 'num' }, size(a.bytes)), ' in ', h('code', null, a.dir), '.');
  }

  // ---------- state ----------

  var MIN_TURNS = 40;  // below this a p95 is not quoted; the API's min_turns replaces it
  function takeLimits(d) { if (d && d.min_turns > 0) MIN_TURNS = d.min_turns; }
  function retention() { // "7 days", or '' when the journal does not know
    var hrs = S.ov ? S.ov.retention_hours : 0;
    if (!(hrs > 0)) return '';
    return hrs % 24 === 0 ? plural(hrs / 24, 'day') : plural(hrs, 'hour');
  }

  var S = {
    ov: null,          // last overview
    ovAt: null,        // when it was read
    token: 0,          // render generation
    cleanup: null,     // tears down the current view
    callView: null,    // {id, player, dirty}
    issueFilter: { status: 'all', area: 'all' }
  };
  var view = document.getElementById('view');

  function api(path, opts) {
    var ctl = new AbortController();
    var timer = setTimeout(function () { ctl.abort(); }, 12000);
    opts = opts || {};
    opts.signal = ctl.signal;
    return fetch(path, opts).then(function (r) {
      clearTimeout(timer);
      if (!r.ok) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          var e = new Error(j && j.error ? j.error : 'HTTP ' + r.status);
          e.status = r.status;
          throw e;
        });
      }
      return r.json();
    }, function (e) {
      clearTimeout(timer);
      throw new Error(e && e.name === 'AbortError' ? 'no answer within 12 s' : 'the journal could not be reached');
    });
  }

  function loadOverview() {
    return api('/api/overview').then(function (ov) {
      S.ov = ov; S.ovAt = new Date(); takeLimits(ov);
      updateTop();
      return ov;
    });
  }

  // ---------- tooltip ----------

  var tip = h('div', { class: 'tip', role: 'tooltip' });
  tip.hidden = true;
  document.body.appendChild(tip);
  var tipSticky = false;

  function showTip(content, x, y) {
    clear(tip);
    addKids(tip, [content]);
    tip.hidden = false;
    var r = tip.getBoundingClientRect(), vw = document.documentElement.clientWidth, vh = window.innerHeight;
    var left = x + 14, top = y + 16;
    if (left + r.width > vw - 8) left = Math.max(8, x - r.width - 14);
    if (left + r.width > vw - 8) left = Math.max(8, vw - r.width - 8);
    if (top + r.height > vh - 8) top = Math.max(8, y - r.height - 12);
    tip.style.left = left + 'px';
    tip.style.top = top + 'px';
  }
  function hideTip() { tip.hidden = true; tipSticky = false; }
  // bindTip: fn(el, event) returns a node, or null for no tip.
  function bindTip(root, selector, fn) {
    function at(e) {
      var el = selector ? (e.target.closest ? e.target.closest(selector) : null) : root;
      if (!el || !root.contains(el)) { if (!tipSticky) hideTip(); return; }
      var c = fn(el, e);
      if (!c) { hideTip(); return; }
      showTip(c, e.clientX, e.clientY);
      tipSticky = e.pointerType === 'touch';
    }
    root.addEventListener('pointermove', function (e) { if (e.pointerType !== 'touch') at(e); });
    root.addEventListener('pointerdown', function (e) { if (e.pointerType === 'touch') at(e); });
    root.addEventListener('pointerleave', function (e) { if (e.pointerType !== 'touch') hideTip(); });
    root.addEventListener('focusin', function (e) {
      var el = selector ? e.target.closest(selector) : root;
      if (!el) return;
      var c = fn(el, e);
      if (!c) return;
      var r = el.getBoundingClientRect();
      showTip(c, r.left + r.width / 2, r.bottom - 8);
    });
    root.addEventListener('focusout', hideTip);
  }
  document.addEventListener('pointerdown', function (e) { if (tipSticky && !tip.hidden) hideTip(); }, true);
  window.addEventListener('scroll', function () { if (!tip.hidden) hideTip(); }, { passive: true });

  function tipRows(title, rows, foot) {
    var box = h('div', null, title ? h('div', { class: 'tip-title' }, title) : null);
    var tb = h('div', { class: 'tip-rows' });
    rows.forEach(function (r) {
      if (!r) return;
      tb.appendChild(h('span', { class: 'tip-k' }, r[2] ? h('i', { class: 'key ' + r[2] }) : null, r[0]));
      tb.appendChild(h('span', { class: 'tip-v' }, r[1]));
    });
    box.appendChild(tb);
    if (foot) box.appendChild(h('div', { class: 'tip-foot' }, foot));
    return box;
  }

  // ---------- shared pieces ----------

  var VERDICT = {
    'met': { cls: 'v-met', label: 'met', say: 'Even the unlucky end of what this sample allows is inside 5%.' },
    'provisional': { cls: 'v-prov', label: 'provisional', say: 'p95 is inside the target, but the sample still allows a true share over 5%.' },
    'not met': { cls: 'v-miss', label: 'not met', say: 'More than one turn in twenty is over the target.' },
    'too few': { cls: 'v-few', label: 'too few turns', say: 'Too few turns to say either way.' }
  };
  function verdictOf(st) { return VERDICT[st && st.verdict] || { cls: 'v-few', label: (st && st.verdict) || 'unknown', say: '' }; }

  function glyph(kind) { // small status shapes, so a verdict is never colour alone
    var g = s('svg', { class: 'glyph', viewBox: '0 0 12 12', width: 12, height: 12, 'aria-hidden': 'true' });
    if (kind === 'v-met' || kind === 'fixed') g.appendChild(s('path', { d: 'M2.2 6.4l2.5 2.5 5-5.6', fill: 'none', 'stroke-width': 1.8, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', stroke: 'currentColor' }));
    else if (kind === 'v-prov' || kind === 'watching') { g.appendChild(s('circle', { cx: 6, cy: 6, r: 4.2, fill: 'none', stroke: 'currentColor', 'stroke-width': 1.5 })); g.appendChild(s('path', { d: 'M6 1.8a4.2 4.2 0 0 1 0 8.4z', fill: 'currentColor' })); }
    else if (kind === 'v-miss') g.appendChild(s('path', { d: 'M3 3l6 6M9 3l-6 6', fill: 'none', 'stroke-width': 1.8, 'stroke-linecap': 'round', stroke: 'currentColor' }));
    else if (kind === 'open') g.appendChild(s('circle', { cx: 6, cy: 6, r: 3.6, fill: 'currentColor' }));
    else if (kind === 'decision') g.appendChild(s('path', { d: 'M6 1.5l4.5 4.5L6 10.5 1.5 6z', fill: 'currentColor' }));
    else if (kind === 'dropped') g.appendChild(s('path', { d: 'M2.5 6h7', fill: 'none', 'stroke-width': 1.8, 'stroke-linecap': 'round', stroke: 'currentColor' }));
    else g.appendChild(s('circle', { cx: 6, cy: 6, r: 3.6, fill: 'none', stroke: 'currentColor', 'stroke-width': 1.5, 'stroke-dasharray': '2 1.6' }));
    return g;
  }

  function verdictChip(st) {
    var v = verdictOf(st);
    return h('span', { class: 'chip ' + v.cls, title: v.say }, glyph(v.cls), v.label);
  }

  // The 95% interval for the true share of turns over target: the honest part.
  var GAUGE_MAX = 30; // percent shown; an interval that runs past it is drawn open-ended
  function intervalText(st) { return pct(st.miss_low) + '–' + pct(st.miss_high) + '%'; }
  function intervalGauge(st, opts) {
    opts = opts || {};
    var v = verdictOf(st);
    var lo = Math.min(st.miss_low, GAUGE_MAX), hi = Math.min(st.miss_high, GAUGE_MAX);
    var open = st.miss_high > GAUGE_MAX;
    var g = h('span', { class: 'gauge ' + v.cls + (open ? ' open' : ''), 'aria-hidden': 'true' },
      h('i', { class: 'gauge-bar', style: { left: (lo / GAUGE_MAX * 100) + '%', width: Math.max((hi - lo) / GAUGE_MAX * 100, 2) + '%' } }),
      h('i', { class: 'gauge-tick', style: { left: (5 / GAUGE_MAX * 100) + '%' } }));
    var title = 'With ' + st.over + ' of ' + plural(st.turns, 'turn') + ' over, the true share of turns over target is between ' +
      pct(st.miss_low) + '% and ' + pct(st.miss_high) + '% (95% interval). The target allows 5%, marked by the tick.';
    return h('span', { class: 'interval' + (opts.compact ? ' compact' : ''), title: title },
      g, h('span', { class: 'interval-t num' }, intervalText(st)), opts.compact ? null : h('span', { class: 'interval-l' }, ' of turns over, 95% interval'));
  }

  // Under min_turns a p95 is the worst turn or the one before it: say "worst", hollow.
  function tail(st) {
    return st.turns >= MIN_TURNS ? { label: 'p95', v: st.p95, solid: true } : { label: 'worst', v: st.max, solid: false };
  }
  function tailMark(st, target) {
    var t = tail(st);
    return h('i', { class: 'mk ' + (t.solid ? 'solid' : 'hollow') + (t.v > target ? ' over' : ''), 'aria-hidden': 'true' });
  }

  function commitLink(hash, repo) {
    if (!repo || !hash) return h('code', { class: 'hash' }, hash || '–');
    return h('a', { class: 'hash', href: repo.replace(/\/+$/, '') + '/commit/' + encodeURIComponent(hash), target: '_blank', rel: 'noopener', title: 'Open commit ' + hash + ' in a new tab' }, hash);
  }

  function section(title, note) {
    var body = h('div', { class: 'sec-body' });
    addKids(body, Array.prototype.slice.call(arguments, 2));
    return h('section', { class: 'sec' },
      h('div', { class: 'sec-head' }, h('h2', null, title), note ? h('p', { class: 'sec-note' }, note) : null),
      body);
  }

  function fig(label, value, opts) {
    opts = opts || {};
    return h('div', { class: 'fig' + (opts.cls ? ' ' + opts.cls : '') },
      h('div', { class: 'fig-l' }, opts.mark || null, label),
      h('div', { class: 'fig-v num' }, value, opts.unit ? h('span', { class: 'fig-u' }, opts.unit) : null));
  }

  function ratingPips(r) {
    var box = h('span', { class: 'pips', title: 'Your rating: ' + r + ' of 5', role: 'img', 'aria-label': 'Your rating: ' + r + ' of 5' });
    for (var i = 1; i <= 5; i++) box.appendChild(h('i', { class: i <= r ? 'on' : '' }));
    return box;
  }
  function icon(kind, title) {
    var g = s('svg', { class: 'ico', viewBox: '0 0 14 14', width: 14, height: 14, role: 'img', 'aria-label': title });
    g.appendChild(s('title', null, title));
    if (kind === 'rec') g.appendChild(s('path', { d: 'M1.5 7v0M4 4.5v5M6.5 2v10M9 5v4M11.5 3.5v7', fill: 'none', stroke: 'currentColor', 'stroke-width': 1.6, 'stroke-linecap': 'round' }));
    else g.appendChild(s('path', { d: 'M2.5 3h9M2.5 6h9M2.5 9h9M2.5 12h5', fill: 'none', stroke: 'currentColor', 'stroke-width': 1.4, 'stroke-linecap': 'round' }));
    return g;
  }

  // ---------- stages: hue says who owns the time ----------

  var STAGES = {
    wait: { short: 'waiting into the pause', owner: 'own' },
    pause: { short: 'waiting for the pause', owner: 'own' },
    asr: { short: 'recognizer final', owner: 'asr' },
    llm_headers: { short: 'chat model, headers', owner: 'llm' },
    llm_token: { short: 'chat model, first token', owner: 'llm' },
    clause: { short: 'first clause', owner: 'own' },
    cache: { short: 'played from memory', owner: 'own' },
    tts: { short: 'synthesizer, first audio', owner: 'tts' },
    hold: { short: 'held for confirmation', owner: 'own' },
    other: { short: 'not accounted for', owner: 'other' }
  };
  var OWNERS = { own: "golive's own waiting", asr: 'Recognizer', llm: 'Chat model', tts: 'Synthesizer', other: 'Not accounted for' };
  function stageCls(key) { return 'st-' + (STAGES[key] ? key : 'other'); }
  function stageShort(st) { return STAGES[st.key] ? STAGES[st.key].short : st.label; }

  function stageLegend() {
    function item(key, text) { return h('span', { class: 'lg-item' }, h('i', { class: 'key ' + stageCls(key) }), text); }
    return h('div', { class: 'legend', 'aria-label': 'Stage colours' },
      h('span', { class: 'lg-group' }, h('span', { class: 'lg-owner' }, "golive's own waiting"), item('wait', 'pause, first clause, hold')),
      h('span', { class: 'lg-group' }, h('span', { class: 'lg-owner' }, 'Recognizer'), item('asr', 'final')),
      h('span', { class: 'lg-group' }, h('span', { class: 'lg-owner' }, 'Chat model'), item('llm_headers', 'headers'), item('llm_token', 'first token')),
      h('span', { class: 'lg-group' }, h('span', { class: 'lg-owner' }, 'Synthesizer'), item('tts', 'first audio')),
      h('span', { class: 'lg-group' }, item('other', 'not accounted for')));
  }

  // ---------- markdown subset (retrospectives, a few note fields) ----------

  function inline(parent, text, repo) {
    var re = /(\*\*[^*]+\*\*|`[^`]+`|ISS-\d{3,})/g, last = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
      var tok = m[0];
      if (tok.charAt(0) === '*') {
        var b = h('strong'); inline(b, tok.slice(2, -2), repo); parent.appendChild(b);
      } else if (tok.charAt(0) === '`') {
        var body = tok.slice(1, -1);
        if (/^[0-9a-f]{7}$/.test(body)) parent.appendChild(commitLink(body, repo));
        else parent.appendChild(h('code', null, body));
      } else {
        parent.appendChild(h('a', { class: 'iss-ref', href: '#/issues/' + tok }, tok));
      }
      last = m.index + tok.length;
    }
    if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
    return parent;
  }
  function markdown(text, repo) {
    var out = h('div', { class: 'prose' });
    var lines = String(text).replace(/\r/g, '').split('\n');
    var para = [], list = null;
    function flushPara() { if (para.length) { out.appendChild(inline(h('p'), para.join(' '), repo)); para = []; } }
    function flushList() { list = null; }
    lines.forEach(function (line) {
      var m;
      if (!line.trim()) { flushPara(); flushList(); return; }
      if ((m = /^(#{1,4})\s+(.*)$/.exec(line))) {
        flushPara(); flushList();
        out.appendChild(inline(h(m[1].length <= 2 ? 'h3' : 'h4'), m[2], repo));
        return;
      }
      if ((m = /^\s*[-*]\s+(.*)$/.exec(line))) {
        flushPara();
        if (!list) { list = h('ul'); out.appendChild(list); }
        list.appendChild(inline(h('li'), m[1], repo));
        return;
      }
      if (list && /^\s+\S/.test(line) && list.lastChild) { // a bullet carried over a line
        list.lastChild.appendChild(document.createTextNode(' '));
        inline(list.lastChild, line.trim(), repo);
        return;
      }
      flushList();
      para.push(line.trim());
    });
    flushPara();
    return out;
  }

  // ---------- top bar ----------

  function updateTop() {
    var ov = S.ov;
    var b = document.getElementById('top-build'), r = document.getElementById('top-rec'), u = document.getElementById('top-updated');
    clear(b); clear(r); clear(u);
    if (ov) {
      addKids(b, ['build ', ov.build ? commitLink(ov.build, ov.repo_url) : '–']);
      r.className = 'top-item rec ' + (ov.recording ? 'on' : 'off');
      addKids(r, [h('i', { 'aria-hidden': 'true' }), ov.recording ? 'recording on' : 'recording off']);
      r.title = ov.recording
        ? (ov.archive ? 'New calls are recorded and archived for good.' : 'New calls are recorded' + (retention() ? ' and kept for ' + retention() : '') + '.')
        : 'Calls are not being recorded' + (ov.archive ? '; their conversation and measurements are still archived.' : '.');
    }
    if (S.ovAt) addKids(u, ['updated ', h('span', { class: 'num' }, clock(S.ovAt))]);
  }
  function setNav(name) {
    Array.prototype.forEach.call(document.querySelectorAll('.nav a'), function (a) {
      if (a.getAttribute('data-nav') === name) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    });
  }

  // ---------- loading and errors ----------

  function showLoading(what) {
    clear(view);
    view.appendChild(h('p', { class: 'state', role: 'status' }, 'Reading ' + what + '…'));
  }
  function showError(err, retry, what) {
    clear(view);
    view.appendChild(h('div', { class: 'state state-err', role: 'alert' },
      h('p', { class: 'state-h' }, (what || 'The journal') + ' could not be read.'),
      h('p', null, 'The API said: ' + (err && err.message ? err.message : 'nothing') + '. Check that the journal is running on this address.'),
      h('button', { type: 'button', class: 'btn', onclick: retry }, 'Try again')));
  }

  // ====================================================================
  // Overview
  // ====================================================================

  function standing(title, sub, st, target) {
    var t = tail(st);
    // A scale centred on the target: half the target to one and a half times it.
    var lo = target * 0.5, hi = target * 1.5;
    function x(v) { return Math.max(0, Math.min(100, (v - lo) / (hi - lo) * 100)); }
    var scale = h('div', { class: 'scale', role: 'img', 'aria-label': 'p50 ' + st.p50 + ' ms, p90 ' + st.p90 + ' ms, ' + t.label + ' ' + t.v + ' ms against the ' + target + ' ms target' },
      h('i', { class: 'scale-line' }),
      h('i', { class: 'scale-range', style: { left: x(st.p50) + '%', width: Math.max(x(t.v) - x(st.p50), 0) + '%' } }),
      h('i', { class: 'scale-target', style: { left: x(target) + '%' } }),
      h('i', { class: 'scale-p90', style: { left: x(st.p90) + '%' } }),
      h('i', { class: 'mk p50', style: { left: x(st.p50) + '%' } }),
      h('i', { class: 'mk ' + (t.solid ? 'solid' : 'hollow') + (t.v > target ? ' over' : '') + (t.v > hi ? ' off' : ''), style: { left: x(t.v) + '%' } }),
      h('span', { class: 'scale-lab', style: { left: '0%' } }, num(lo)),
      h('span', { class: 'scale-lab mid', style: { left: '50%' } }, num(target) + ' ms target'),
      h('span', { class: 'scale-lab end', style: { left: '100%' } }, num(hi)));
    return h('div', { class: 'standing' },
      h('div', { class: 'standing-h' },
        h('div', null, h('h3', null, title), h('p', { class: 'standing-sub' }, sub)),
        verdictChip(st)),
      scale,
      h('div', { class: 'figs' },
        fig('p50', num(st.p50), { mark: h('i', { class: 'mk p50' }) }),
        fig('p90', num(st.p90)),
        fig(t.label, num(t.v), { mark: tailMark(st, target), cls: t.v > target ? 'is-over' : '' }),
        t.solid ? fig('worst', num(st.max)) : null,
        fig('over ' + num(target), st.over + ' of ' + st.turns)),
      h('p', { class: 'standing-int' }, intervalGauge(st)),
      t.solid ? null : h('p', { class: 'standing-note' }, 'Under ' + MIN_TURNS + ' turns a p95 is the worst turn or the one before it (it would read ' + num(st.p95) + '), so the worst turn is shown instead.'),
      h('p', { class: 'standing-note' }, 'Turn judged over → first audio, as the latency console counts it: p50 ', h('span', { class: 'num' }, num(st.reply_p50)), ', p90 ', h('span', { class: 'num' }, num(st.reply_p90)), ', p95 ', h('span', { class: 'num' }, num(st.reply_p95)), ' ms.'));
  }

  function trendChart(host, ov) {
    var calls = ov.calls.slice(0, 60).reverse(); // oldest on the left
    var target = ov.target_ms;
    function draw() {
      var W = Math.floor(host.clientWidth);
      if (!W) return;
      host._w = W;
      clear(host);
      var narrow = W < 560;
      var H = narrow ? 220 : 250, mL = 44, mR = 50, mT = 22, mB = 46;
      var pw = W - mL - mR, ph = H - mT - mB, n = calls.length;
      if (!n) { host.appendChild(h('p', { class: 'empty' }, 'No calls yet. Make a call and it appears here.')); return; }
      var slot = pw / n;
      var tails = calls.map(function (c) { return tail(c.summary).v; });
      var p50s = calls.map(function (c) { return c.summary.p50; });
      var cap = Math.ceil(target * 1.5 / 200) * 200;
      var yMax = Math.min(cap, Math.max(Math.ceil(Math.max.apply(null, tails.concat([target * 1.1])) / 200) * 200, target + 200));
      var yMin = Math.max(0, Math.min(Math.floor((Math.min.apply(null, p50s) - 60) / 200) * 200, target - 400));
      function y(v) { return mT + ph - (Math.min(v, yMax) - yMin) / (yMax - yMin) * ph; }
      function cx(i) { return mL + slot * (i + 0.5); }

      var svg = s('svg', { class: 'trend', width: W, height: H, viewBox: '0 0 ' + W + ' ' + H, role: 'img', 'aria-label': 'Latency of each call over time against the ' + target + ' ms target' });

      // build bands: alternating, bracketed under the axis
      var runs = [], i;
      for (i = 0; i < n; i++) {
        if (runs.length && runs[runs.length - 1].build === calls[i].build) runs[runs.length - 1].to = i;
        else runs.push({ build: calls[i].build, from: i, to: i });
      }
      runs.forEach(function (r, k) {
        var x0 = mL + slot * r.from, w = slot * (r.to - r.from + 1);
        if (k % 2 === 1) svg.appendChild(s('rect', { class: 'band', x: x0, y: mT, width: w, height: ph }));
        svg.appendChild(s('path', { class: 'bracket', d: 'M' + (x0 + 3) + ' ' + (H - 20) + 'v4H' + (x0 + w - 3) + 'v-4' }));
        if (w >= 50) svg.appendChild(s('text', { class: 'tk build', x: x0 + w / 2, y: H - 4, 'text-anchor': 'middle' }, r.build || '?'));
      });

      // grid and axis
      for (var v = yMin; v <= yMax + 1; v += 200) {
        if (v === target) continue;
        svg.appendChild(s('line', { class: 'grid', x1: mL, x2: W - mR, y1: y(v), y2: y(v) }));
        svg.appendChild(s('text', { class: 'tk', x: mL - 8, y: y(v) + 4, 'text-anchor': 'end' }, num(v)));
      }
      svg.appendChild(s('line', { class: 'target', x1: mL, x2: W - mR, y1: y(target), y2: y(target) }));
      svg.appendChild(s('text', { class: 'tk strong', x: mL - 8, y: y(target) + 4, 'text-anchor': 'end' }, num(target)));
      svg.appendChild(s('text', { class: 'tk strong', x: W - mR + 6, y: y(target) + 4, 'text-anchor': 'start' }, 'target'));
      svg.appendChild(s('text', { class: 'tk', x: mL - 8, y: mT - 10, 'text-anchor': 'end' }, 'ms'));

      // time labels: as many as fit
      var every = Math.max(1, Math.ceil(64 / slot)), lastDay = '';
      calls.forEach(function (c, i) {
        if ((n - 1 - i) % every !== 0) return;
        var w = when(c.started_at), day = w.slice(0, 5), time = w.slice(6);
        svg.appendChild(s('text', { class: 'tk', x: cx(i), y: mT + ph + 15, 'text-anchor': 'middle' }, (day !== lastDay && slot * every >= 84 ? day + ' ' : '') + time));
        lastDay = day;
      });

      // marks
      calls.forEach(function (c, i) {
        var st = c.summary, t = tail(st), x = cx(i), off = t.v > yMax;
        var g = s('g', { class: 'call-mark' });
        g.appendChild(s('line', { class: 'range', x1: x, x2: x, y1: y(st.p50), y2: off ? mT - 2 : y(t.v) }));
        g.appendChild(s('circle', { class: 'p50', cx: x, cy: y(st.p50), r: 4.5 }));
        var cls = 'tailmark ' + (t.solid ? 'solid' : 'hollow') + (t.v > target ? ' over' : '');
        if (off) {
          g.appendChild(s('path', { class: cls, d: 'M' + (x - 5) + ' ' + (mT + 4) + 'L' + x + ' ' + (mT - 5) + 'L' + (x + 5) + ' ' + (mT + 4) + 'z' }));
          var anchor = x > W - mR - 50 ? 'end' : 'start';
          g.appendChild(s('text', { class: 'tk strong', x: x + (anchor === 'end' ? -9 : 9), y: mT + 3, 'text-anchor': anchor }, num(t.v)));
        } else {
          g.appendChild(s('circle', { class: cls, cx: x, cy: y(t.v), r: 5 }));
        }
        svg.appendChild(g);
        var a = s('a', { href: '#/call/' + c.id, class: 'hit', 'aria-label': 'Call ' + when(c.started_at) + ', build ' + c.build + ', p50 ' + st.p50 + ' ms, ' + t.label + ' ' + t.v + ' ms' });
        a.appendChild(s('rect', { x: mL + slot * i, y: mT - 12, width: slot, height: ph + 30 }));
        a._call = c;
        svg.appendChild(a);
      });
      host.appendChild(svg);
    }
    bindTip(host, 'a.hit', function (el) {
      var c = el._call, st = c.summary, t = tail(st);
      return tipRows(when(c.started_at), [
        ['build', c.build || '–'],
        ['turns', String(st.turns)],
        ['p50', num(st.p50) + ' ms'],
        ['p90', num(st.p90) + ' ms'],
        [t.solid ? 'p95' : 'worst turn', num(t.v) + ' ms'],
        t.solid ? ['worst turn', num(st.max) + ' ms'] : null,
        ['over ' + num(target), st.over + ' of ' + st.turns],
        ['verdict', verdictOf(st).label]
      ], t.solid ? null : 'Under ' + MIN_TURNS + ' turns: no p95 worth quoting.');
    });
    draw();
    var ro = new ResizeObserver(function () { if (Math.floor(host.clientWidth) !== host._w) draw(); });
    ro.observe(host);
    return function () { ro.disconnect(); };
  }

  function buildsTable(ov) {
    var target = ov.target_ms;
    var head = ['Build', 'Calls', 'Turns', 'p50', 'p90', 'p95', 'Worst', 'Over', 'Share over, 95% interval', 'Verdict'];
    var cls = ['', 'r', 'r', 'r', 'r', 'r', 'r', 'r', '', ''];
    var table = h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, head.map(function (t, i) { return h('th', { class: cls[i], scope: 'col' }, t); }))));
    var tb = h('tbody');
    ov.builds.forEach(function (b) {
      var st = b.summary, few = st.turns < MIN_TURNS;
      var span = when(b.first) === when(b.last) ? when(b.first) : when(b.first) + ' to ' + when(b.last).slice(when(b.first).slice(0, 5) === when(b.last).slice(0, 5) ? 6 : 0);
      tb.appendChild(h('tr', { class: b.build === ov.build ? 'is-current' : '' },
        h('th', { scope: 'row' }, commitLink(b.build, ov.repo_url), h('span', { class: 'sub num' }, span)),
        h('td', { class: 'r num', 'data-l': 'calls' }, b.calls),
        h('td', { class: 'r num', 'data-l': 'turns' }, st.turns),
        h('td', { class: 'r num', 'data-l': 'p50' }, num(st.p50)),
        h('td', { class: 'r num', 'data-l': 'p90' }, num(st.p90)),
        h('td', { class: 'r num' + (few ? ' dim' : (st.p95 > target ? ' over' : '')), 'data-l': 'p95', title: few ? 'Under ' + MIN_TURNS + ' turns a p95 is the worst turn or the one before it.' : null }, few ? '(' + num(st.p95) + ')' : num(st.p95)),
        h('td', { class: 'r num' + (st.max > target ? ' over' : ''), 'data-l': 'worst' }, num(st.max)),
        h('td', { class: 'r num', 'data-l': 'over' }, st.over),
        h('td', { class: 'b-int' }, intervalGauge(st, { compact: true })),
        h('td', { class: 'b-verdict' }, verdictChip(st))));
    });
    table.appendChild(tb);
    return h('div', { class: 'tbl-wrap' }, table);
  }

  // ---------- playing a call straight from the list ----------
  //
  // One recording at a time, in an <audio> that belongs to no row: the list is
  // redrawn every half minute, and what is playing must not stop for that. The
  // buttons are found again by the call they name each time there is
  // something to show.
  var rowPlay = { id: null, audio: null, failed: null };
  function stopRowPlay() {
    var a = rowPlay.audio;
    rowPlay = { id: null, audio: null, failed: null };
    if (a) { a.pause(); a.removeAttribute('src'); try { a.load(); } catch (e) { /* nothing to unload */ } }
    paintRowPlay();
  }
  function toggleRowPlay(c) {
    var a = rowPlay.audio;
    if (a && rowPlay.id === c.id) {
      if (a.paused) a.play().catch(function () { rowPlay.failed = c.id; paintRowPlay(); }); else a.pause();
      return;
    }
    stopRowPlay();
    a = new Audio('/api/calls/' + encodeURIComponent(c.id) + '/audio.wav');
    a.preload = 'auto';
    rowPlay = { id: c.id, audio: a, failed: null };
    ['play', 'playing', 'pause', 'timeupdate', 'durationchange', 'ended', 'error'].forEach(function (ev) {
      a.addEventListener(ev, function () {
        if (rowPlay.audio !== a) return;
        if (ev === 'ended') { a.currentTime = 0; }
        if (ev === 'error') { rowPlay.failed = c.id; }
        paintRowPlay();
      });
    });
    a.play().catch(function () { if (rowPlay.audio === a) { rowPlay.failed = c.id; paintRowPlay(); } });
    paintRowPlay();
  }
  function paintRowPlay() {
    var a = rowPlay.audio;
    Array.prototype.forEach.call(document.querySelectorAll('.row-play'), function (b) {
      var mine = !!a && b.getAttribute('data-call') === rowPlay.id;
      var playing = mine && !a.paused && !a.ended;
      var begun = mine && (playing || a.currentTime > 0);
      var failed = rowPlay.failed === b.getAttribute('data-call');
      b.classList.toggle('is-playing', playing);
      b.classList.toggle('is-begun', begun);
      b.classList.toggle('is-failed', failed);
      b.setAttribute('aria-pressed', playing ? 'true' : 'false');
      var label = b.getAttribute('data-label');
      b.setAttribute('aria-label', (playing ? 'Pause' : 'Play') + ' ' + label);
      // The button stays one size, so nothing in the row moves when it is
      // pressed; where the recording has got to is the line under the row,
      // and in words here.
      var at = begun ? mss(a.currentTime) + ' of ' + mss(isFinite(a.duration) ? a.duration : +b.getAttribute('data-len')) : '';
      b.title = failed ? 'This recording could not be played' : (playing ? 'Pause (' + at + ')' : (begun ? 'Play on from ' + at : 'Play this call’s recording'));
      b.querySelector('.row-play-t').textContent = failed ? 'could not be played' : at;
      var row = b.closest('.call-row');
      if (row) {
        row.classList.toggle('now-playing', begun);
        row.style.setProperty('--played', begun && a.duration ? Math.min(1, a.currentTime / a.duration) : 0);
      }
    });
  }
  function rowPlayButton(c) {
    var b = h('button', { type: 'button', class: 'row-play', 'data-call': c.id, 'data-len': c.duration_s,
      'data-label': 'the recording of the call of ' + when(c.started_at) },
      h('i', { class: 'pp', 'aria-hidden': 'true' }), h('span', { class: 'row-play-t vh', 'aria-live': 'off' }));
    b.addEventListener('click', function (e) { e.preventDefault(); e.stopPropagation(); toggleRowPlay(c); });
    return b;
  }

  function callsList(ov) {
    var target = ov.target_ms;
    var list = h('div', { class: 'calls', role: 'list' });
    list.appendChild(h('div', { class: 'calls-head', 'aria-hidden': 'true' },
      h('span', null, 'When'), h('span', null, 'Build'), h('span', { class: 'r' }, 'Length'), h('span', { class: 'r' }, 'Turns'),
      h('span', { class: 'r' }, 'p50'), h('span', { class: 'r' }, 'p95 / worst'), h('span', { class: 'r' }, 'Over'), h('span', null, 'Verdict, share over'), h('span', null, '')));
    ov.calls.forEach(function (c) {
      var st = c.summary, t = tail(st);
      var marks = h('span', { class: 'c-marks' },
        c.has_audio ? rowPlayButton(c) : null,
        c.has_retro ? icon('retro', 'Retrospective written') : null,
        c.rating ? ratingPips(c.rating) : null);
      // The row is not itself a link: a button cannot sit inside one. The date
      // is the link, and a click anywhere else on the row follows it.
      var row = h('div', { class: 'call-row' + (st.over ? ' has-over' : ''), role: 'listitem' },
        h('a', { class: 'c-when num', href: '#/call/' + c.id }, when(c.started_at)),
        h('span', { class: 'c-build num' }, c.build || '–'),
        h('span', { class: 'c-dur num r', 'data-l': 'length' }, dur(c.duration_s)),
        h('span', { class: 'c-turns num r', 'data-l': 'turns' }, st.turns),
        h('span', { class: 'c-p50 num r', 'data-l': 'p50' }, num(st.p50)),
        h('span', { class: 'c-tail num r' + (t.v > target ? ' over' : ''), 'data-l': t.label, title: t.solid ? 'p95' : 'Worst turn: under ' + MIN_TURNS + ' turns there is no p95 worth quoting.' }, tailMark(st, target), num(t.v), h('span', { class: 'c-tail-l' }, t.solid ? ' p95' : ' worst')),
        h('span', { class: 'c-over num r', 'data-l': 'over' }, st.over),
        h('span', { class: 'c-verdict' }, verdictChip(st), intervalGauge(st, { compact: true })),
        marks,
        c.worst ? h('span', { class: 'c-worst' }, h('i', { class: 'sq-over', 'aria-hidden': 'true' }), c.worst) : null);
      row.addEventListener('click', function (e) {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        if (e.target.closest && e.target.closest('a, button')) return;
        if (String(window.getSelection && window.getSelection()) !== '') return; // selecting text is not a click
        location.hash = '#/call/' + c.id;
      });
      list.appendChild(row);
    });
    if (!ov.calls.length) list.appendChild(h('p', { class: 'empty' }, 'No calls yet.'));
    setTimeout(paintRowPlay, 0); // a list redrawn while a call plays picks its place up
    return h('div', null, list,
      h('p', { class: 'keyline' },
        h('span', null, h('i', { class: 'mk solid' }), ' p95, ' + MIN_TURNS + ' turns or more'),
        h('span', null, h('i', { class: 'mk hollow' }), ' worst turn, under ' + MIN_TURNS + ' turns'),
        h('span', null, h('i', { class: 'key-play', 'aria-hidden': 'true' }, h('i', { class: 'pp' })), ' recording kept: play it here'),
        h('span', null, icon('retro', 'Retrospective written'), ' retrospective written'),
        h('span', null, ratingPips(3), ' your rating')));
  }

  function renderOverview(ov) {
    var target = ov.target_ms;
    var keepY = window.scrollY;
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    clear(view);
    var latest = ov.builds.filter(function (b) { return b.build === ov.build; })[0] || ov.builds[0];
    var trendHost = h('div', { class: 'trend-host' });

    view.appendChild(h('div', { class: 'lede' },
      h('h1', null, 'p95 ≤ ' + num(target) + ' ms, caller’s last sound → first audio'),
      h('p', null, 'The target. At p95 it means at most one turn in twenty may be over. The phone leg (exchange, gateway, carrier) is on top and is not measured.')));

    view.appendChild(section('Where it stands', 'Figures are pooled per build: a turn on another build went through other code.',
      h('div', { class: 'standings' },
        latest ? standing('Build ' + latest.build, plural(latest.calls, 'call') + ', ' + plural(latest.summary.turns, 'turn') + (latest.build === ov.build ? ', running now' : ', latest with calls'), latest.summary, target) : h('p', { class: 'empty' }, 'No calls yet.'),
        ov.recent && ov.recent.turns ? standing('Last ' + ov.recent.turns + ' turns', 'the most recent calls, across builds', ov.recent, target) : null)));

    view.appendChild(section('Call by call', 'One column per call, oldest on the left, bracketed by build.',
      h('div', { class: 'legend' },
        h('span', { class: 'lg-item' }, h('i', { class: 'mk p50' }), 'p50'),
        h('span', { class: 'lg-item' }, h('i', { class: 'mk solid' }), 'p95 (' + MIN_TURNS + ' turns or more)'),
        h('span', { class: 'lg-item' }, h('i', { class: 'mk hollow' }), 'worst turn (under ' + MIN_TURNS + ' turns a p95 is only that)'),
        h('span', { class: 'lg-item' }, h('i', { class: 'mk solid over' }), h('i', { class: 'mk hollow over' }), 'over target')),
      trendHost));

    view.appendChild(section('Builds', 'Calls on one build, pooled.', buildsTable(ov)));
    var callsSec = section('Calls', plural(ov.calls.length, 'call') + ', newest first. A row opens the call.', callsList(ov));
    var arch = archiveLine(ov.archive);
    if (arch) callsSec.querySelector('.sec-body').appendChild(arch);
    view.appendChild(callsSec);

    S.cleanup = trendChart(trendHost, ov);
    window.scrollTo(0, keepY);
  }

  function routeOverview(fresh) {
    var token = ++S.token;
    setNav('overview');
    document.title = 'Call journal';
    if (S.ov && !fresh) { renderOverview(S.ov); }
    else if (!S.ov) showLoading('the journal');
    return loadOverview().then(function (ov) {
      if (token !== S.token) return;
      renderOverview(ov);
    }).catch(function (e) {
      if (token !== S.token) return;
      if (!S.ov) showError(e, function () { routeOverview(true); });
      else markStale(e);
    });
  }
  function markStale(e) {
    var u = document.getElementById('top-updated');
    clear(u);
    addKids(u, [h('span', { class: 'stale' }, 'not reachable since '), h('span', { class: 'num' }, S.ovAt ? clock(S.ovAt) : '–')]);
  }

  // ====================================================================
  // Call
  // ====================================================================

  // ----- the recording -----

  var MARKERS = {
    interrupted: { name: 'answer cut by the caller', shape: 'tri' },
    dropped: { name: 'prepared answer dropped', shape: 'ring' },
    tts_slow: { name: 'slow synthesizer', shape: 'dia' },
    hedge_refused: { name: 'second synthesis refused', shape: 'diao' },
    over: { name: 'turn over target', shape: 'none' }
  };

  function Player(call, audioInfo, target) {
    var self = this;
    var D = Math.max(audioInfo.duration_s || 0, call.duration_s || 0, 1);
    var peaks = null, viewT0 = 0, viewSpan = D, followed = true;
    var turns = (call.turns || []).filter(function (t) { return t.last_sound_s >= 0 && t.audio_out_s >= 0; });
    var markers = (call.markers || []).filter(function (m) { return m.t_s >= 0; });

    var audio = h('audio', { preload: 'metadata', src: '/api/calls/' + encodeURIComponent(call.id) + '/audio.wav' });
    var playBtn = h('button', { type: 'button', class: 'btn play', 'aria-label': 'Play' }, h('i', { class: 'pp', 'aria-hidden': 'true' }), h('span', null, 'Play'));
    var timeEl = h('span', { class: 'ptime num' }, '0:00.0 / ' + pos(D));
    var canvas = h('canvas', { class: 'wave' });
    var head = h('i', { class: 'playhead', 'aria-hidden': 'true' });
    var stage = h('div', { class: 'pstage', tabindex: '0', role: 'slider', 'aria-label': 'Position in the recording', 'aria-valuemin': '0', 'aria-valuemax': String(Math.round(D)), 'aria-valuenow': '0' },
      canvas, head,
      h('span', { class: 'lane-l', style: { top: '20px' } }, 'Caller'),
      h('span', { class: 'lane-l', style: { top: '80px' } }, 'Assistant'));
    var mini = h('div', { class: 'pmini', title: 'The whole call. Click to move.' });
    var miniWin = h('i', { class: 'pmini-win' });
    var miniHead = h('i', { class: 'pmini-head' });
    turns.forEach(function (t) {
      mini.appendChild(h('i', { class: 'pmini-turn' + (t.over ? ' over' : ''), style: { left: (t.last_sound_s / D * 100) + '%', width: Math.max((t.audio_out_s - t.last_sound_s) / D * 100, 0.3) + '%' } }));
    });
    mini.appendChild(miniWin); mini.appendChild(miniHead);
    var msg = h('p', { class: 'pmsg', role: 'status' });

    function seg(label, options, current, onpick) {
      var box = h('span', { class: 'seg', role: 'group', 'aria-label': label });
      options.forEach(function (o) {
        var b = h('button', { type: 'button', 'aria-pressed': String(o.v === current) }, o.t);
        b.addEventListener('click', function () {
          Array.prototype.forEach.call(box.children, function (c) { c.setAttribute('aria-pressed', 'false'); });
          b.setAttribute('aria-pressed', 'true');
          onpick(o.v);
        });
        b._v = o.v;
        box.appendChild(b);
      });
      box.set = function (v) { Array.prototype.forEach.call(box.children, function (c) { c.setAttribute('aria-pressed', String(c._v === v)); }); };
      return box;
    }
    var rateSeg = seg('Playback speed', [{ t: '1×', v: 1 }, { t: '1.5×', v: 1.5 }, { t: '2×', v: 2 }], 1, function (v) { audio.playbackRate = v; });
    var zoomOpts = [{ t: 'Whole call', v: 0 }];
    if (D > 45) zoomOpts.push({ t: '30 s', v: 30 });
    if (D > 15) zoomOpts.push({ t: '10 s', v: 10 });
    var zoomSeg = seg('Window shown', zoomOpts, 0, function (v) { setZoom(v, audio.currentTime); });

    this.el = h('div', { class: 'player' },
      h('div', { class: 'pbar' }, playBtn, timeEl,
        h('span', { class: 'pbar-r' }, h('span', { class: 'pbar-l' }, 'Speed'), rateSeg, h('span', { class: 'pbar-l' }, 'Show'), zoomSeg)),
      mini, stage, msg,
      h('div', { class: 'legend plegend' },
        h('span', { class: 'lg-item' }, h('i', { class: 'gapkey' }), 'last sound → first audio (the wait)'),
        h('span', { class: 'lg-item' }, h('i', { class: 'gapkey over' }), 'over ' + num(target) + ' ms'),
        h('span', { class: 'lg-item' }, h('i', { class: 'mshape tri' }), 'answer cut by the caller'),
        h('span', { class: 'lg-item' }, h('i', { class: 'mshape ring' }), 'prepared answer dropped'),
        h('span', { class: 'lg-item' }, h('i', { class: 'mshape dia' }), 'slow synthesizer'),
        h('span', { class: 'lg-item' }, h('i', { class: 'mshape diao' }), 'second synthesis refused'),
        h('span', { class: 'lg-item keys' }, 'Space plays, ← → move 2 s')),
      audio);

    var H = 138, LANE = [[18, 60], [76, 118]], STRIP = 16;
    function X(t) { return (t - viewT0) / viewSpan * stage.clientWidth; }
    function T(x) { return viewT0 + x / stage.clientWidth * viewSpan; }

    function draw() {
      var W = stage.clientWidth;
      if (!W) return;
      var dpr = window.devicePixelRatio || 1;
      canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr);
      canvas.style.height = H + 'px';
      var g = canvas.getContext('2d');
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
      g.clearRect(0, 0, W, H);
      var ink = cssVar('--ink'), ink2 = cssVar('--ink-2'), ink3 = cssVar('--ink-3'), line = cssVar('--line'),
        over = cssVar('--over'), tts = cssVar('--st-tts'), surface = cssVar('--surface'), gap = cssVar('--gap');
      g.font = '11px ' + cssVar('--mono');
      g.textBaseline = 'middle';

      // time axis
      var steps = [1, 2, 5, 10, 15, 30, 60, 120, 300], step = steps[steps.length - 1];
      for (var i = 0; i < steps.length; i++) { if (steps[i] * W / viewSpan >= 64) { step = steps[i]; break; } }
      g.fillStyle = ink3; g.strokeStyle = line; g.lineWidth = 1;
      for (var t = Math.ceil(viewT0 / step) * step; t <= viewT0 + viewSpan + 0.001; t += step) {
        var x = Math.round(X(t)) + 0.5;
        g.beginPath(); g.moveTo(x, STRIP); g.lineTo(x, 122); g.stroke();
        var lab = Math.floor(t / 60) + ':' + pad(Math.round(t % 60));
        var tw = g.measureText(lab).width;
        g.fillText(lab, Math.min(Math.max(x - tw / 2, 0), W - tw), 130);
      }
      // lane baselines
      LANE.forEach(function (l) {
        var mid = (l[0] + l[1]) / 2 + 0.5;
        g.beginPath(); g.moveTo(0, mid); g.lineTo(W, mid); g.stroke();
      });

      // the wait of each answered turn: from the caller's last sound to the first audio
      turns.forEach(function (tn) {
        var x0 = X(tn.last_sound_s), x1 = X(tn.audio_out_s);
        if (x1 < 0 || x0 > W) return;
        var w = Math.max(x1 - x0, 2);
        g.globalAlpha = tn.over ? 0.26 : 0.2;
        g.fillStyle = tn.over ? over : gap;
        g.fillRect(x0, STRIP, w, 122 - STRIP);
        g.globalAlpha = 1;
        g.fillStyle = tn.over ? over : gap;
        g.fillRect(x0, STRIP, tn.over ? 1.5 : 1, 122 - STRIP);
        g.fillRect(x0 + w - (tn.over ? 1.5 : 1), STRIP, tn.over ? 1.5 : 1, 122 - STRIP);
        if (w >= 30 || viewSpan <= 45) {
          var lab = String(tn.total_ms), tw = g.measureText(lab).width;
          if (tw + 6 <= w || viewSpan <= 45) {
            g.fillStyle = surface; g.globalAlpha = 0.85;
            g.fillRect(x0 + w / 2 - tw / 2 - 3, 61, tw + 6, 14);
            g.globalAlpha = 1;
            g.fillStyle = tn.over ? cssVar('--over-ink') : ink;
            g.fillText(lab, x0 + w / 2 - tw / 2, 68.5);
          }
        }
      });

      // waveforms
      if (peaks) {
        [[peaks.caller, LANE[0]], [peaks.assistant, LANE[1]]].forEach(function (pair) {
          var arr = pair[0], l = pair[1], mid = (l[0] + l[1]) / 2, half = (l[1] - l[0]) / 2, n = arr.length;
          var pd = peaks.duration_s || D;
          g.fillStyle = ink2;
          g.beginPath();
          for (var x = 0; x < W; x++) {
            var a = Math.floor(T(x) / pd * n), b = Math.floor(T(x + 1) / pd * n), m = 0;
            if (a >= n || b < 0) continue;
            a = Math.max(a, 0); b = Math.min(Math.max(b, a + 1), n);
            for (var k = a; k < b; k++) if (arr[k] > m) m = arr[k];
            var hgt = Math.max(Math.sqrt(m / 1000) * half, 0.5);
            g.rect(x, mid - hgt, 1, hgt * 2);
          }
          g.fill();
        });
      }

      // markers
      markers.forEach(function (m) {
        var x = X(m.t_s), k = MARKERS[m.kind] || { shape: 'tick' };
        if (x < -6 || x > W + 6 || k.shape === 'none') return;
        var y = 8;
        g.lineWidth = 1.5;
        if (k.shape === 'tri') { g.fillStyle = ink; g.beginPath(); g.moveTo(x - 4, y - 4); g.lineTo(x + 4, y - 4); g.lineTo(x, y + 4); g.closePath(); g.fill(); }
        else if (k.shape === 'ring') { g.strokeStyle = ink3; g.beginPath(); g.arc(x, y, 3.2, 0, Math.PI * 2); g.stroke(); }
        else if (k.shape === 'dia' || k.shape === 'diao') {
          g.beginPath(); g.moveTo(x, y - 5); g.lineTo(x + 4.5, y); g.lineTo(x, y + 5); g.lineTo(x - 4.5, y); g.closePath();
          if (k.shape === 'dia') { g.fillStyle = tts; g.fill(); } else { g.fillStyle = surface; g.fill(); g.strokeStyle = tts; g.stroke(); }
        } else { g.strokeStyle = ink3; g.beginPath(); g.moveTo(x, y - 4); g.lineTo(x, y + 4); g.stroke(); }
      });
      g.lineWidth = 1;
      placeHead();
      miniWin.style.left = (viewT0 / D * 100) + '%';
      miniWin.style.width = (viewSpan / D * 100) + '%';
      miniWin.hidden = viewSpan >= D;
    }

    function placeHead() {
      var t = audio.currentTime || 0, x = X(t), W = stage.clientWidth;
      head.style.transform = 'translateX(' + Math.round(x) + 'px)';
      head.hidden = x < -1 || x > W + 1;
      miniHead.style.left = (t / D * 100) + '%';
      timeEl.textContent = pos(t) + ' / ' + pos(D);
      stage.setAttribute('aria-valuenow', String(Math.round(t)));
      stage.setAttribute('aria-valuetext', pos(t) + ' of ' + pos(D));
      if (self.ontime) self.ontime(t, !audio.paused && !audio.ended);
    }

    function setView(t0, span) {
      viewSpan = Math.min(span, D);
      viewT0 = Math.max(0, Math.min(t0, D - viewSpan));
      draw();
    }
    function setZoom(span, around) {
      zoomSeg.set(span);
      if (!span) setView(0, D);
      else setView((around || 0) - span * 0.3, span);
    }

    var raf = 0;
    function tick() {
      raf = 0;
      var t = audio.currentTime || 0;
      if (viewSpan < D && !audio.paused && (t > viewT0 + viewSpan * 0.92 || t < viewT0)) setView(t - viewSpan * 0.12, viewSpan);
      else placeHead();
      if (!audio.paused) raf = requestAnimationFrame(tick);
    }
    function setPlaying(on) {
      playBtn.classList.toggle('is-playing', on);
      playBtn.setAttribute('aria-label', on ? 'Pause' : 'Play');
      playBtn.lastChild.textContent = on ? 'Pause' : 'Play';
      if (on && !raf) raf = requestAnimationFrame(tick);
      if (self.onstate) self.onstate(on);
    }
    audio.addEventListener('play', function () { setPlaying(true); });
    audio.addEventListener('pause', function () { setPlaying(false); });
    audio.addEventListener('ended', function () { setPlaying(false); });
    audio.addEventListener('seeked', function () { tick(); });
    audio.addEventListener('timeupdate', function () { if (audio.paused) placeHead(); });
    audio.addEventListener('error', function () { msg.textContent = 'The recording could not be loaded. The timings below do not depend on it.'; });

    function seek(t) {
      t = Math.max(0, Math.min(t, D - 0.05));
      try { audio.currentTime = t; } catch (e) { /* not seekable yet */ }
      if (viewSpan < D && (t < viewT0 || t > viewT0 + viewSpan)) setView(t - viewSpan * 0.3, viewSpan);
      else placeHead();
    }
    function toggle() { if (audio.paused) { var p = audio.play(); if (p && p.catch) p.catch(function () {}); } else audio.pause(); }
    playBtn.addEventListener('click', toggle);

    // click or drag to seek
    var dragging = false;
    stage.addEventListener('pointerdown', function (e) {
      if (e.button) return;
      dragging = true;
      try { stage.setPointerCapture(e.pointerId); } catch (err) { /* older browsers */ }
      seek(T(e.clientX - stage.getBoundingClientRect().left));
    });
    stage.addEventListener('pointermove', function (e) { if (dragging) seek(T(e.clientX - stage.getBoundingClientRect().left)); });
    function endDrag() { dragging = false; }
    stage.addEventListener('pointerup', endDrag);
    stage.addEventListener('pointercancel', endDrag);
    mini.addEventListener('pointerdown', function (e) {
      var r = mini.getBoundingClientRect(), t = (e.clientX - r.left) / r.width * D;
      if (viewSpan < D) setView(t - viewSpan / 2, viewSpan);
      seek(t);
    });

    bindTip(stage, null, function (el, e) {
      if (e.type === 'focusin') return null;
      var r = stage.getBoundingClientRect(), px = e.clientX - r.left, t = T(px);
      var near = markers.filter(function (m) { return Math.abs(X(m.t_s) - px) <= 6; });
      var tn = turns.filter(function (q) { return px >= X(q.last_sound_s) - 3 && px <= Math.max(X(q.audio_out_s), X(q.last_sound_s) + 2) + 3; })[0];
      var box = h('div', null, h('div', { class: 'tip-title num' }, pos(t)));
      if (tn) box.appendChild(h('div', { class: 'tip-line' + (tn.over ? ' over' : '') }, 'Turn ' + tn.n + ': ' + num(tn.total_ms) + ' ms from last sound to first audio' + (tn.over ? ', over target' : '')));
      near.forEach(function (m) { if (m.kind !== 'over') box.appendChild(h('div', { class: 'tip-line' }, m.label)); });
      return box;
    });

    this.audio = audio;
    this.isPlaying = function () { return !audio.paused && !audio.ended; };
    this.toggle = toggle;
    this.nudge = function (d) { seek((audio.currentTime || 0) + d); };
    this.pause = function () { audio.pause(); };
    this.playTurn = function (tn) {
      var start = Math.max(0, tn.last_sound_s - 1.5);
      if (D > 15) { zoomSeg.set(10); setView(start - 1, 10); }
      seek(start);
      var p = audio.play(); if (p && p.catch) p.catch(function () {});
    };
    this.playFrom = function (t) { seek(t); var p = audio.play(); if (p && p.catch) p.catch(function () {}); };
    this.currentTurnWindow = function (tn) { var t = audio.currentTime || 0; return t >= tn.last_sound_s - 1.6 && t <= tn.audio_out_s + 12; };
    this.redraw = draw;
    this.destroy = function () { audio.pause(); audio.removeAttribute('src'); try { audio.load(); } catch (e) { /* nothing to unload */ } if (raf) cancelAnimationFrame(raf); ro.disconnect(); mq.removeEventListener('change', draw); };

    var ro = new ResizeObserver(function () { draw(); });
    ro.observe(stage);
    var mq = window.matchMedia('(prefers-color-scheme: dark)');
    mq.addEventListener('change', draw);

    var n = Math.max(1200, Math.min(8000, Math.round(D * 20)));
    api('/api/calls/' + encodeURIComponent(call.id) + '/peaks?n=' + n).then(function (p) { peaks = p; draw(); })
      .catch(function () { msg.textContent = 'The waveform could not be read; the recording still plays.'; });
  }

  // ----- the conversation -----

  function flash(el) {
    el.classList.remove('flash');
    void el.offsetWidth; // restart the animation
    el.classList.add('flash');
    setTimeout(function () { el.classList.remove('flash'); }, 1800);
  }
  function mss(sec) { return sec >= 0 ? Math.floor(sec / 60) + ':' + pad(Math.floor(sec % 60)) : '–'; }

  function conversation(call, target, player, state) {
    var lines = (call.transcript || []).filter(function (l) { return l && l.text; });
    var api = { el: null, hasTurn: function () { return false; }, showTurn: function () {} };
    if (!lines.length) { api.el = h('p', { class: 'empty' }, 'No conversation was kept for this call.'); return api; }

    var answeredOnly = false, byTurn = {}, rows = [], lastUser = 0, now = null, prevKey = -1;
    var box = h('div', { class: 'cv', tabindex: '0', role: 'group', 'aria-label': 'The conversation, in order' });

    lines.forEach(function (l, i) {
      var caller = l.who === 'caller', answered = l.turn > 0;
      if (!caller && answered && l.total_ms > 0) { // the wait before this answer: last sound → first audio
        var gap = h('div', { class: 'cv-gap' + (l.over ? ' over' : ''), title: 'The caller’s last sound to the first audio of the answer.' },
          h('i', { class: 'cv-gap-line', style: { height: Math.round(12 + Math.min(l.total_ms, 3200) / 100) + 'px' }, 'aria-hidden': 'true' }),
          h('span', { class: 'cv-gap-t' },
            l.over ? h('i', { class: 'sq-over', 'aria-hidden': 'true' }) : null,
            h('span', { class: 'num' }, num(l.total_ms) + ' ms'),
            l.over ? h('span', { class: 'cv-gap-over num' }, '+' + num(l.total_ms - target) + ' over') : h('span', { class: 'cv-gap-l' }, 'wait')));
        gap._answered = true;
        box.appendChild(gap);
        rows.push({ el: gap, answered: true, gap: true });
      }
      var note = l.kind === 'greeting' ? 'opening line' : (caller && !answered ? 'not answered' : (!caller && l.interrupted ? 'cut short by the caller' : ''));
      var playBtn = null;
      if (player && l.t_s >= 0) {
        playBtn = h('button', { type: 'button', class: 'cv-play', 'aria-label': 'Play from ' + mss(l.t_s) + ', ' + (caller ? 'caller' : 'assistant'), title: 'Play from here' }, h('i', { class: 'pp', 'aria-hidden': 'true' }));
        playBtn.addEventListener('click', function () { lastUser = 0; player.playFrom(Math.max(0, l.t_s - (caller ? 4 : 0.2))); });
      }
      var turnBtn = null;
      if (answered && caller) {
        turnBtn = h('button', { type: 'button', class: 'cv-turn num', title: 'Show turn ' + l.turn + ' in the waterfall' }, 'turn ' + l.turn);
        turnBtn.addEventListener('click', function () { if (state && state.openTurn) state.openTurn(l.turn); });
      }
      var el = h('div', { class: 'cv-line ' + (caller ? 'caller' : 'assistant') + (caller && !answered ? ' unanswered' : '') + (l.over ? ' over' : '') },
        h('span', { class: 'cv-time' }, playBtn, h('span', { class: 'num' }, mss(l.t_s))),
        h('span', { class: 'cv-who' }, caller ? 'Caller' : 'Assistant'),
        h('div', { class: 'cv-text' }, h('p', { lang: 'zh' }, l.text), note ? h('span', { class: 'cv-note' }, note) : null),
        h('span', { class: 'cv-side' }, turnBtn));
      box.appendChild(el);
      // When a line counts as "being said": an answer from its first audio; a caller
      // line a little before its last sound, which is the only time the log has for it.
      var key = caller ? l.t_s - Math.min(4, 0.6 + 0.25 * l.text.length) : l.t_s;
      if (!(key > prevKey)) key = prevKey + 0.05; // keys only ever go forward
      prevKey = key;
      var row = { el: el, line: l, answered: answered, key: key };
      rows.push(row);
      if (answered && caller) byTurn[l.turn] = row;
    });

    function shown(r) { return !answeredOnly || r.answered; }
    function applyFilter() { rows.forEach(function (r) { r.el.hidden = !shown(r); }); count.textContent = countText(); }
    function countText() {
      var said = rows.filter(function (r) { return !r.gap && shown(r); }).length;
      return plural(said, 'line') + (answeredOnly ? ' in ' : ', ') + plural(Object.keys(byTurn).length, 'answered turn');
    }
    function plain() {
      return rows.filter(function (r) { return !r.gap && shown(r); }).map(function (r) {
        return '[' + mss(r.line.t_s) + '] ' + (r.line.who === 'caller' ? 'Caller' : 'Assistant') + ': ' + r.line.text;
      }).join('\n');
    }
    function scrollTo(el) { // inside the box only; the page stays where it is
      var b = box.getBoundingClientRect(), e = el.getBoundingClientRect();
      box.scrollTop += (e.top - b.top) - (box.clientHeight - e.height) / 2;
    }

    // controls
    var filter = h('span', { class: 'seg', role: 'group', 'aria-label': 'What to show' });
    [['Everything said', false], ['Answered turns only', true]].forEach(function (o) {
      var b = h('button', { type: 'button', 'aria-pressed': String(o[1] === answeredOnly) }, o[0]);
      b.addEventListener('click', function () {
        answeredOnly = o[1];
        Array.prototype.forEach.call(filter.children, function (c) { c.setAttribute('aria-pressed', String(c === b)); });
        applyFilter();
      });
      filter.appendChild(b);
    });
    var count = h('span', { class: 'cv-count' }, '');
    var copied = h('span', { class: 'cv-copied', role: 'status' });
    var copyBtn = h('button', { type: 'button', class: 'btn btn-quiet' }, 'Copy as text');
    var fallback = h('textarea', { class: 'cv-plain', readonly: true, rows: 6, 'aria-label': 'The conversation as plain text' });
    fallback.hidden = true;
    function said(msg) { copied.textContent = msg; clearTimeout(said.t); said.t = setTimeout(function () { copied.textContent = ''; }, 4000); }
    function selectInstead(text) {
      fallback.hidden = false;
      fallback.value = text;
      fallback.focus(); fallback.select();
      var ok = false;
      try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
      if (ok) { fallback.hidden = true; copyBtn.focus(); said('Copied'); }
      else said('Selected below: copy it with Ctrl+C or ⌘C');
    }
    copyBtn.addEventListener('click', function () {
      var text = plain();
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function () { fallback.hidden = true; said('Copied'); }, function () { selectInstead(text); });
      } else selectInstead(text);
    });

    // following the recording: the line whose time has most recently passed
    ['wheel', 'touchstart', 'touchmove', 'keydown', 'pointerdown'].forEach(function (ev) {
      box.addEventListener(ev, function (e) { if (!(e.target.closest && e.target.closest('.cv-play'))) lastUser = Date.now(); }, { passive: true });
    });
    if (player) {
      var spoken = rows.filter(function (r) { return !r.gap && r.line.t_s >= 0; });
      player.ontime = function (t, playing) {
        var lo = 0, hi = spoken.length - 1, at = -1;
        while (lo <= hi) { var mid = (lo + hi) >> 1; if (spoken[mid].key <= t) { at = mid; lo = mid + 1; } else hi = mid - 1; }
        var r = at >= 0 ? spoken[at] : null;
        if (r === now) return;
        if (now) now.el.classList.remove('now');
        now = r;
        if (!r) return;
        r.el.classList.add('now');
        if (playing && !r.el.hidden && Date.now() - lastUser > 4000) scrollTo(r.el);
      };
    }

    api.hasTurn = function (n) { return !!byTurn[n]; };
    api.showTurn = function (n) {
      var r = byTurn[n];
      if (!r) return;
      lastUser = Date.now();
      api.el.scrollIntoView({ block: 'start' });
      scrollTo(r.el);
      flash(r.el);
    };
    api.el = h('div', { class: 'cv-wrap' },
      h('div', { class: 'cv-bar' }, filter, count, h('span', { class: 'cv-bar-r' }, copied, copyBtn)),
      fallback, box);
    applyFilter();
    return api;
  }

  // ----- the turn waterfall -----

  function niceTicks(max, target) {
    var step = max <= 1600 ? 200 : (max <= 3200 ? 400 : 800), out = [];
    for (var v = 0; v <= max; v += step) out.push(v);
    if (out.indexOf(target) < 0) out.push(target);
    return out;
  }

  function stageBar(stages, axisMax, extra) {
    var bar = h('span', { class: 'bar' + (extra || '') });
    stages.forEach(function (st) {
      if (st.ms <= 0) return;
      var e = h('span', { class: 'sg ' + stageCls(st.key), style: { width: (st.ms / axisMax * 100) + '%' } });
      e._st = st;
      bar.appendChild(e);
    });
    return bar;
  }
  function stageTip(el) {
    var st = el._st;
    if (!st) return null;
    return h('div', null, h('div', { class: 'tip-big num' }, num(st.ms) + ' ms'), h('div', { class: 'tip-line' }, h('i', { class: 'key ' + stageCls(st.key) }), st.label));
  }
  function stageList(stages, total) {
    var box = h('div', { class: 'stlist' });
    stages.forEach(function (st) {
      box.appendChild(h('span', { class: 'stl-k' }, h('i', { class: 'key ' + stageCls(st.key) }), st.label));
      box.appendChild(h('span', { class: 'stl-v num' }, num(st.ms)));
    });
    if (total !== undefined) {
      box.appendChild(h('span', { class: 'stl-k stl-sum' }, 'Last sound → first audio'));
      box.appendChild(h('span', { class: 'stl-v stl-sum num' }, num(total)));
    }
    return box;
  }

  function badges(t) {
    var b = h('span', { class: 'badges' });
    function add(text, cls, title) { b.appendChild(h('span', { class: 'badge' + (cls ? ' ' + cls : ''), title: title }, text)); }
    if (t.speculative) add('prepared', 'quiet', 'Prepared in the pause: the answer was begun before the turn was judged over.');
    else add('no head start', 'warn', 'The answer was begun only when the turn closed.');
    if (t.cache_hit) add('from memory', '', 'Opened with words kept in the phrase cache: no synthesis in the caller’s silence.');
    if (t.interrupted) add('cut', '', 'The caller spoke over the answer' + (t.heard_ms ? ' after ' + (t.heard_ms / 1000).toFixed(1) + ' s' : '') + '.');
    if (t.hedge_refused) add('2nd synth refused', 'tts', 'A second synthesis request was wanted and refused by the request budget.');
    else if (t.hedge_asked && t.hedge_winner === 2) add('2nd synth won', 'tts', 'A second synthesis connection was asked and spoke first.');
    else if (t.hedge_asked) add('2nd synth asked', 'tts', 'A second synthesis connection was asked; the first still spoke first.');
    if (t.winner_copy) add('copy ' + t.winner_copy + (t.copies ? '/' + t.copies : ''), 'quiet num', 'Copy ' + t.winner_copy + (t.copies ? ' of ' + t.copies : '') + ' of the completion answered first.');
    return b;
  }

  function waterfall(call, target, player, state) {
    var turns = call.turns || [];
    if (!turns.length) return h('p', { class: 'empty' }, 'No answered turns on this call.');
    var maxTotal = Math.max.apply(null, turns.map(function (t) { return t.total_ms; }));
    var axisMax = Math.min(Math.ceil(Math.max(maxTotal, target * 1.12) / 200) * 200, target * 2);
    var ticks = niceTicks(axisMax, target);
    var grid = ticks.filter(function (v) { return v > 0 && v < axisMax && v !== target; }).map(function (v) {
      var p = (v / axisMax * 100).toFixed(3) + '%';
      return 'linear-gradient(90deg, transparent calc(' + p + ' - 0.5px), var(--line) calc(' + p + ' - 0.5px), var(--line) calc(' + p + ' + 0.5px), transparent calc(' + p + ' + 0.5px))';
    }).join(',');
    var wf = h('div', { class: 'wf', style: { '--tx': (target / axisMax * 100) + '%', '--grid': grid || 'none' } });

    var axis = h('span', { class: 'wf-track wf-axis-track' });
    ticks.forEach(function (v) {
      if (v !== target && Math.abs(v - target) / axisMax < 0.07) return;
      var minor = v !== target && ticks.indexOf(v) % 2 === 1;
      axis.appendChild(h('span', { class: 'wf-tick num' + (v === target ? ' is-target' : '') + (minor ? ' minor' : ''), style: { left: (v / axisMax * 100) + '%' } }, num(v), v === target ? h('span', { class: 't-word' }, ' target') : null));
    });
    wf.appendChild(h('div', { class: 'wf-line wf-axis', 'aria-hidden': 'true' }, h('span', { class: 'wf-n' }, h('span', { class: 't-word' }, 'turn')), axis, h('span', { class: 'wf-total' }, 'ms'), h('span', { class: 'badges' })));

    var rows = h('div', { class: 'wf-rows' }), opens = {};
    if (state) state.openTurn = function (n) { if (opens[n]) opens[n](); };
    turns.forEach(function (t) {
      var clipped = t.total_ms > axisMax;
      var row = h('div', { class: 'wf-row' + (t.over ? ' over' : ''), id: 'turn-' + t.n });
      var detail = null;
      var btn = h('button', { type: 'button', class: 'wf-line', 'aria-expanded': 'false', 'aria-label': 'Turn ' + t.n + ', ' + t.total_ms + ' ms' + (t.over ? ', over target' : '') + '. Show what was said.' },
        h('span', { class: 'wf-n num' }, t.n),
        h('span', { class: 'wf-track' }, stageBar(t.stages, axisMax, clipped ? ' clipped' : '')),
        h('span', { class: 'wf-total num', title: clipped ? 'Longer than the axis shows.' : null }, t.over ? h('i', { class: 'sq-over', 'aria-hidden': 'true' }) : null, num(t.total_ms)),
        badges(t));
      row.appendChild(btn);
      if (t.over) row.appendChild(h('div', { class: 'wf-cause' }, h('span', { class: 'wf-over-by num' }, '+' + num(t.total_ms - target) + ' over'), t.cause ? ' ' + t.cause : ''));
      function setOpen(on) {
        btn.setAttribute('aria-expanded', String(on));
        row.classList.toggle('open', on);
        if (on && !detail) { detail = turnDetail(t, call, target, player, state); row.appendChild(detail); }
        if (detail) detail.hidden = !on;
      }
      btn.addEventListener('click', function () { setOpen(btn.getAttribute('aria-expanded') !== 'true'); });
      opens[t.n] = function () { setOpen(true); row.scrollIntoView({ block: 'center' }); btn.focus({ preventScroll: true }); flash(row); };
      rows.appendChild(row);
    });
    wf.appendChild(rows);
    bindTip(wf, '.sg', stageTip);
    return h('div', null, stageLegend(), wf);
  }

  function turnDetail(t, call, target, player, state) {
    var said = h('div', { class: 'said' },
      h('div', { class: 'said-b' }, h('div', { class: 'said-who' }, 'Caller'), h('p', { class: 'said-t', lang: 'zh' }, t.caller_text || '(nothing recognized)')),
      h('div', { class: 'said-b' }, h('div', { class: 'said-who' }, 'Assistant' + (t.interrupted ? ', as far as it was heard' + (t.heard_ms ? ' (' + (t.heard_ms / 1000).toFixed(1) + ' s)' : '') : '')),
        h('p', { class: 'said-t', lang: 'zh' }, t.assistant_text || '(nothing said)')));
    var facts = h('p', { class: 'wf-facts' },
      'Turn judged over → first audio: ', h('span', { class: 'num' }, num(t.reply_ms)), ' ms',
      t.pause_ms ? [', after a ', h('span', { class: 'num' }, num(t.pause_ms)), ' ms pause'] : null,
      t.last_sound_s >= 0 ? ['. Last sound at ', h('span', { class: 'num' }, pos(t.last_sound_s)), ' in the call.'] : '.');
    var play = null;
    if (player && t.last_sound_s >= 0) {
      play = h('button', { type: 'button', class: 'btn' }, h('i', { class: 'pp', 'aria-hidden': 'true' }), h('span', null, 'Play this turn'));
      play.addEventListener('click', function () {
        if (player.isPlaying() && player.currentTurnWindow(t)) player.pause();
        else player.playTurn(t);
      });
      play._turn = t;
      play.classList.add('play-turn');
    }
    var back = null;
    if (state && state.conv && state.conv.hasTurn(t.n)) {
      back = h('button', { type: 'button', class: 'linkbtn' }, 'Show in the conversation');
      back.addEventListener('click', function () { state.conv.showTurn(t.n); });
    }
    return h('div', { class: 'wf-detail' }, said, h('div', { class: 'wf-side' }, stageList(t.stages, t.total_ms), facts, h('div', { class: 'wf-acts' }, play, back)));
  }

  // ----- where the time goes -----

  function stageMedians(call) {
    var st = call.stage_medians || [];
    if (!st.length) return h('p', { class: 'empty' }, 'No stages to show.');
    var sum = st.reduce(function (a, x) { return a + x.ms; }, 0) || 1;
    var bar = stageBar(st, sum, ' big');
    var by = {};
    st.forEach(function (x) { var o = (STAGES[x.key] || STAGES.other).owner; by[o] = (by[o] || 0) + x.ms; });
    var list = h('div', { class: 'medlist' });
    st.forEach(function (x) {
      list.appendChild(h('div', { class: 'med' },
        h('span', { class: 'med-v num' }, num(x.ms)),
        h('span', { class: 'med-k' }, h('i', { class: 'key ' + stageCls(x.key) }), x.label),
        h('span', { class: 'med-p num' }, Math.round(x.ms / sum * 100) + '%')));
    });
    var owners = h('p', { class: 'owners' });
    ['own', 'asr', 'llm', 'tts', 'other'].forEach(function (o) {
      if (!by[o]) return;
      owners.appendChild(h('span', { class: 'owner' }, h('i', { class: 'key ' + stageCls(o === 'own' ? 'wait' : o === 'llm' ? 'llm_token' : o) }), OWNERS[o] + ' ', h('strong', { class: 'num' }, num(by[o])), ' ms'));
    });
    var wrap = h('div', { class: 'medians' }, h('div', { class: 'wf-track big-track' }, bar), owners, list,
      h('p', { class: 'sec-note' }, 'Each stage’s own median over the turns that have it; they add up to ' + num(sum) + ' ms and need not equal the median turn (' + num(call.summary.p50) + ' ms).'));
    bindTip(wrap, '.sg', stageTip);
    return wrap;
  }

  // ----- what else happened -----

  function median(a) { if (!a.length) return 0; var b = a.slice().sort(function (x, y) { return x - y; }), n = b.length; return n % 2 ? b[(n - 1) / 2] : Math.round((b[n / 2 - 1] + b[n / 2]) / 2); }

  function dotPlot(values, xMax, cls, unitName) {
    var W = 340, H = 118, mL = 6, mR = 10, base = H - 22, pw = W - mL - mR;
    var bins = 52, bw = xMax / bins, counts = [], i;
    for (i = 0; i < bins; i++) counts.push(0);
    values.forEach(function (v) { counts[Math.min(bins - 1, Math.max(0, Math.floor(v / bw)))]++; });
    var peak = Math.max.apply(null, counts) || 1;
    var dy = Math.min(6, (base - 16) / peak), r = Math.min(2.5, dy / 2 - 0.2);
    var svg = s('svg', { class: 'dots ' + cls, viewBox: '0 0 ' + W + ' ' + H, role: 'img', 'aria-label': values.length + ' ' + unitName + ', median ' + median(values) + ' ms, longest ' + Math.max.apply(null, values) + ' ms' });
    svg.appendChild(s('line', { class: 'axis', x1: mL, x2: W - mR, y1: base + 0.5, y2: base + 0.5 }));
    var step = xMax <= 600 ? 100 : 200;
    for (var v = 0; v <= xMax; v += step) {
      var x = mL + v / xMax * pw;
      svg.appendChild(s('line', { class: 'axis', x1: x, x2: x, y1: base, y2: base + 4 }));
      svg.appendChild(s('text', { class: 'tk', x: x, y: base + 16, 'text-anchor': v === 0 ? 'start' : (v >= xMax ? 'end' : 'middle') }, v));
    }
    counts.forEach(function (c, b) {
      if (!c) return;
      var cx = mL + (b + 0.5) / bins * pw;
      for (var k = 0; k < c; k++) svg.appendChild(s('circle', { class: 'dot', cx: cx, cy: base - 3.5 - k * dy, r: r }));
      var hit = s('rect', { class: 'hitbin', x: cx - pw / bins / 2, y: 0, width: pw / bins, height: base });
      hit._bin = [Math.round(b * bw), Math.round((b + 1) * bw) - 1, c];
      svg.appendChild(hit);
    });
    var md = median(values), mx = mL + md / xMax * pw;
    svg.appendChild(s('line', { class: 'median', x1: mx, x2: mx, y1: 12, y2: base }));
    svg.appendChild(s('text', { class: 'tk strong', x: mx + 5, y: 12, 'text-anchor': 'start' }, 'median ' + md));
    var top = Math.max.apply(null, values);
    if (top > md * 1.8) svg.appendChild(s('text', { class: 'tk strong', x: Math.min(mL + top / xMax * pw, W - mR), y: base - 12, 'text-anchor': 'end' }, 'longest ' + top));
    var box = h('div', { class: 'dots-box' }, svg);
    bindTip(box, '.hitbin', function (el) { var b = el._bin; return tipRows(null, [[b[0] + '–' + b[1] + ' ms', plural(b[2], unitName.replace(/s$/, ''))]]); });
    return box;
  }

  function hbars(rows, cls) { // [{label, n}]
    var max = Math.max.apply(null, rows.map(function (r) { return r.n; })) || 1;
    var box = h('div', { class: 'hbars ' + cls });
    rows.forEach(function (r) {
      box.appendChild(h('span', { class: 'hb-l num' }, r.label));
      box.appendChild(h('span', { class: 'hb-t' }, h('i', { style: { width: (r.n / max * 100) + '%' } })));
      box.appendChild(h('span', { class: 'hb-v num' }, r.n));
    });
    return box;
  }

  function copyWins(wins) {
    var max = Math.max.apply(null, wins) || 1;
    var box = h('div', { class: 'cols', role: 'img', 'aria-label': 'Turns won by each copy: ' + wins.join(', ') });
    wins.forEach(function (w, i) {
      box.appendChild(h('span', { class: 'col' }, h('span', { class: 'col-v num' }, w), h('i', { style: { height: Math.max(w / max * 56, w ? 2 : 0) + 'px' } }), h('span', { class: 'col-l num' }, i + 1)));
    });
    return box;
  }

  function chartBox(title, note, body) {
    return h('div', { class: 'chart' }, h('h3', null, title), body, note ? h('p', { class: 'chart-note' }, note) : null);
  }

  function eventsBlock(call) {
    var ev = call.events || {};
    function cnt(n, label, note) { return h('div', { class: 'cnt', title: note || null }, h('span', { class: 'cnt-v num' }, n === undefined ? '–' : n), h('span', { class: 'cnt-l' }, label)); }
    var groups = h('div', { class: 'cnt-groups' },
      h('div', { class: 'cnt-group' }, h('h3', null, 'Listening'), h('div', { class: 'cnts' },
        cnt(ev.utterances, 'utterances'), cnt(ev.early_finals, 'early finals', 'Finals asked for inside a pause.'), cnt(ev.closed_on_final, 'closed on a final', 'Turns closed on words that were already final.'), cnt(ev.interruptions, 'interruptions', 'Answers cut because the caller spoke over them.'))),
      h('div', { class: 'cnt-group' }, h('h3', null, 'Prepared answers'), h('div', { class: 'cnts' },
        cnt(ev.guesses_held, 'guesses held', 'A prepared answer was kept: the caller had finished.'), cnt(ev.guesses_dropped, 'guesses dropped', 'A prepared answer was thrown away: the caller carried on.'))),
      h('div', { class: 'cnt-group' }, h('h3', null, 'Speaking'), h('div', { class: 'cnts' },
        cnt(ev.segments, 'segments synthesized'), cnt(ev.cache_plays, 'played from memory'), cnt(ev.abandoned_tasks, 'synthesis tasks abandoned', 'Started and thrown away at an interruption or a dropped guess.'))),
      h('div', { class: 'cnt-group' }, h('h3', null, 'Second synthesis'), h('div', { class: 'cnts' },
        cnt(ev.hedge_asked, 'asked'), cnt(ev.hedge_won, 'won'), cnt(ev.hedge_refused, 'refused', 'Wanted, and refused by the request budget.'))));

    var charts = h('div', { class: 'charts' });
    var llm = ev.llm_first_tokens || [], tts = ev.tts_first_audios || [];
    var xMax = Math.max(600, Math.ceil(Math.max.apply(null, llm.concat(tts, [0])) / 200) * 200);
    if (llm.length) charts.appendChild(chartBox('Chat model, first token', plural(llm.length, 'completion') + ', ms. One dot each; the fastest copy of each.', dotPlot(llm, xMax, 'c-llm', 'completions')));
    if (tts.length) charts.appendChild(chartBox('Synthesizer, first audio', plural(tts.length, 'segment') + ', ms, on the same scale.', dotPlot(tts, xMax, 'c-tts', 'segments')));
    if (ev.copy_wins && ev.copy_wins.length) charts.appendChild(chartBox('Which copy answered first', 'Turns won by each of ' + ev.copy_wins.length + ' copies of the completion.', copyWins(ev.copy_wins)));
    var pauses = ev.pauses_inside || [];
    if (pauses.length) {
      var buckets = [{ label: '60–79', n: 0 }, { label: '80–99', n: 0 }, { label: '100–119', n: 0 }, { label: '120+', n: 0 }], under = 0;
      pauses.forEach(function (p) { if (p < 60) under++; else if (p < 80) buckets[0].n++; else if (p < 100) buckets[1].n++; else if (p < 120) buckets[2].n++; else buckets[3].n++; });
      if (under) buckets.unshift({ label: 'under 60', n: under });
      charts.appendChild(chartBox('Pauses inside an utterance', plural(pauses.length, 'pause') + ' the caller carried on from, by length in ms.', hbars(buckets, 'c-own')));
    }

    var facts = h('ul', { class: 'facts' });
    if (ev.noise_floor_db) facts.appendChild(h('li', null, 'Noise floor ', h('span', { class: 'num' }, ev.noise_floor_db.toFixed(1)), ' dB', ev.has_idle_level ? [', idle line ', h('span', { class: 'num' }, ev.idle_level_db.toFixed(1)), ' dB'] : null, '.'));
    if ((ev.echo_returns && ev.echo_returns.length) || ev.echo_not_timed) facts.appendChild(h('li', null, 'The assistant’s voice coming back was timed ', plural((ev.echo_returns || []).length, 'time'), (ev.echo_returns || []).length ? [' (', h('span', { class: 'num' }, ev.echo_returns.slice().sort(function (a, b) { return a - b; }).join(', ')), ' ms)'] : null, ' and not timed ', plural(ev.echo_not_timed || 0, 'time'), '.'));
    if (ev.prefix_warm_ms && ev.prefix_warm_ms.length) facts.appendChild(h('li', null, 'Prompt warmed before the first turn: ', h('span', { class: 'num' }, ev.prefix_warm_ms.join(', ')), ' ms.'));

    var warn = null;
    if (call.warnings && call.warnings.length) {
      warn = h('div', { class: 'warns' }, h('h3', null, 'Warnings in the log'), h('ul', null, call.warnings.map(function (w) {
        return h('li', { class: 'warn-' + String(w.level).toLowerCase() },
          h('span', { class: 'warn-t num' }, w.t_s >= 0 ? pos(w.t_s) : '–'),
          h('span', { class: 'warn-lv' }, w.level),
          h('span', { class: 'warn-x' }, w.text),
          w.count > 1 ? h('span', { class: 'warn-n num' }, '×' + w.count) : null);
      })));
    } else warn = h('p', { class: 'sec-note' }, 'No warnings were logged on this call.');
    return h('div', null, groups, charts, facts.childNodes.length ? facts : null, warn);
  }

  // ----- your notes -----

  function feedbackForm(call, fb, state) {
    var rating = fb ? fb.rating : 0;
    var saved = { rating: rating, note: fb ? fb.note : '' };
    var words = ['', 'bad', 'poor', 'fair', 'good', 'natural'];
    var status = h('span', { class: 'fb-status', role: 'status' });
    var note = h('textarea', { id: 'fb-note', rows: 3, maxlength: 4000, placeholder: 'What you heard: a long silence, a wrong word, an answer that came too soon…' });
    note.value = saved.note || '';
    var save = h('button', { type: 'submit', class: 'btn btn-main' }, 'Save notes');
    var rate = h('div', { class: 'rate', role: 'radiogroup', 'aria-label': 'How the call sounded, 1 to 5' });
    function paint() {
      Array.prototype.forEach.call(rate.children, function (b, i) { b.setAttribute('aria-checked', String(i + 1 === rating)); b.classList.toggle('in', i + 1 <= rating); });
      word.textContent = rating ? words[rating] : 'not rated';
      var dirty = rating !== saved.rating || note.value.trim() !== (saved.note || '').trim();
      state.dirty = dirty;
      save.disabled = !dirty;
      if (dirty) { status.textContent = 'Not saved yet'; status.className = 'fb-status dirty'; }
    }
    var word = h('span', { class: 'rate-word' });
    for (var i = 1; i <= 5; i++) (function (v) {
      var b = h('button', { type: 'button', role: 'radio', 'aria-label': v + ', ' + words[v], title: words[v] + (v === rating ? '' : '') }, String(v));
      b.addEventListener('click', function () { rating = rating === v ? 0 : v; paint(); });
      rate.appendChild(b);
    })(i);
    note.addEventListener('input', paint);
    function showSaved(f) {
      status.className = 'fb-status';
      clear(status);
      if (f && f.updated_at) addKids(status, ['Saved ', h('span', { class: 'num' }, when(f.updated_at) + ':' + pad(new Date(f.updated_at).getSeconds()))]);
    }
    var form = h('form', { class: 'fb' },
      h('div', { class: 'fb-row' }, h('span', { class: 'fb-l', id: 'fb-rate-l' }, 'How did it sound?'), rate, word),
      h('label', { class: 'fb-l', for: 'fb-note' }, 'Note'),
      note,
      h('div', { class: 'fb-row' }, save, status));
    form.addEventListener('submit', function (e) {
      e.preventDefault();
      save.disabled = true;
      status.className = 'fb-status'; status.textContent = 'Saving…';
      api('/api/calls/' + encodeURIComponent(call.id) + '/feedback', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ rating: rating, note: note.value }) })
        .then(function (f) {
          saved = { rating: f.rating, note: f.note };
          rating = f.rating; note.value = f.note || '';
          paint(); showSaved(f);
        }).catch(function (err) {
          save.disabled = false;
          status.className = 'fb-status err';
          status.textContent = 'Not saved: ' + err.message;
        });
    });
    paint(); showSaved(fb);
    return form;
  }

  // ----- the call page -----

  function renderCall(d) {
    takeLimits(d);
    var call = d.call, st = call.summary, target = d.target_ms, t = tail(st);
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    clear(view);
    var state = { id: call.id, player: null, dirty: false };
    S.callView = state;

    var calls = S.ov ? S.ov.calls : [], idx = -1;
    calls.forEach(function (c, i) { if (c.id === call.id) idx = i; });
    var newer = idx > 0 ? calls[idx - 1] : null, older = idx >= 0 && idx < calls.length - 1 ? calls[idx + 1] : null;
    function pager(cls) {
      return h('nav', { class: 'pager pg-' + cls, 'aria-label': 'Other calls' },
        older ? h('a', { href: '#/call/' + older.id, rel: 'prev' }, h('span', { class: 'pg-l' }, 'Older call'), h('span', { class: 'num' }, when(older.started_at))) : h('span', { class: 'pg-none' }, 'This is the oldest call'),
        h('a', { href: '#/', class: 'pg-all' }, 'All calls'),
        newer ? h('a', { href: '#/call/' + newer.id, rel: 'next', class: 'pg-next' }, h('span', { class: 'pg-l' }, 'Newer call'), h('span', { class: 'num' }, when(newer.started_at))) : h('span', { class: 'pg-none pg-next' }, 'This is the newest call'));
    }

    var meta = h('dl', { class: 'meta' });
    function m(k, v) { meta.appendChild(h('div', null, h('dt', null, k), h('dd', null, v))); }
    m('Build', commitLink(call.build, d.repo_url));
    m('Length', h('span', { class: 'num' }, dur(call.duration_s)));
    m('Prompt', [h('span', { class: 'num' }, num(call.prompt_chars)), ' characters', call.prompt_sha ? [' ', h('code', { class: 'hash', title: 'Hash of the instructions' }, call.prompt_sha)] : null]);
    if (call.llm_copies || call.keys || call.lanes) m('Chat model', [call.llm_copies ? plural(call.llm_copies, 'copy', 'copies') : null, call.keys ? (call.llm_copies ? ', ' : '') + plural(call.keys, 'key') : null, call.lanes ? ', ' + plural(call.lanes, 'lane') : null]);
    m('Call', h('code', { class: 'hash' }, call.id));

    view.appendChild(h('div', { class: 'call-head' },
      pager('top'),
      h('div', { class: 'call-title' }, h('h1', null, 'Call of ', h('span', { class: 'num' }, when(call.started_at))), verdictChip(st), intervalGauge(st)),
      h('nav', { class: 'jump', 'aria-label': 'On this page' },
        [['Read the conversation', 'sec-conversation', 'jump-main'], ['Turn by turn', 'sec-turns'], ['Where the time goes', 'sec-stages'], ['Retrospective', 'sec-retro'], ['Your notes', 'sec-notes']].map(function (j) {
          var b = h('button', { type: 'button', class: 'linkbtn' + (j[2] ? ' ' + j[2] : '') }, j[0]);
          b.addEventListener('click', function () { var e = document.getElementById(j[1]); if (e) e.scrollIntoView({ block: 'start' }); });
          return b;
        })),
      meta,
      h('div', { class: 'defs' },
        h('div', { class: 'def' },
          h('h3', null, 'Caller’s last sound → first audio', h('span', { class: 'def-tag' }, 'the target’s definition')),
          h('div', { class: 'figs' },
            fig('p50', num(st.p50), { unit: ' ms' }), fig('p90', num(st.p90)),
            fig(t.label, num(t.v), { mark: tailMark(st, target), cls: t.v > target ? 'is-over' : '' }),
            t.solid ? fig('worst', num(st.max)) : null,
            fig('over ' + num(target), st.over + ' of ' + st.turns)),
          t.solid ? null : h('p', { class: 'standing-note' }, plural(st.turns, 'turn') + ': too few for a p95 (it would read ' + num(st.p95) + '), so the worst turn is shown.')),
        h('div', { class: 'def' },
          h('h3', null, 'Turn judged over → first audio', h('span', { class: 'def-tag' }, 'the latency console’s definition')),
          h('div', { class: 'figs' }, fig('p50', num(st.reply_p50), { unit: ' ms' }), fig('p90', num(st.reply_p90)), fig('p95', num(st.reply_p95))),
          h('p', { class: 'standing-note' }, 'Does not include the pause that ended the turn.'))),
      h('p', { class: 'legnote' }, 'The phone leg (exchange, gateway, carrier, both ways) is on top of either figure and is not included.')));

    // recording
    var recBody;
    if (d.audio && d.audio.available) {
      state.player = new Player(call, d.audio, target);
      recBody = state.player.el;
    } else {
      recBody = h('div', { class: 'norec' }, h('p', { class: 'norec-h' }, 'No recording of this call'),
        h('p', null, 'Recording was off when it was made. Everything below comes from the log and is complete without it.'));
    }
    // where the call is kept, and its files to take away
    var ar = d.archive, base = '/api/calls/' + encodeURIComponent(call.id) + '/';
    var kept = h('p', { class: 'archive-line' }, h('span', { class: 'archive-tag' }, 'Archive'), ' ');
    if (ar && ar.archived) {
      addKids(kept, [(ar.audio ? 'Recording and conversation' : 'Conversation and measurements') + ' kept for good, ',
        h('span', { class: 'num' }, size(ar.bytes)), ' in ', h('code', null, ar.dir), '. ']);
    } else {
      addKids(kept, ['Not archived yet: a call is archived when it has ended. ']);
    }
    if (d.audio && d.audio.available) addKids(kept, [h('a', { href: base + 'audio.wav?download=1' }, 'Download recording'), ' · ']);
    addKids(kept, [h('a', { href: base + 'transcript.txt?download=1' }, 'Download transcript')]);
    recBody = h('div', null, recBody, kept);
    view.appendChild(section('Recording', d.audio && d.audio.available ? 'Caller above, assistant below. Each shaded band is one wait: last sound to first audio. Click or drag to move.' : null, recBody));

    function add(id, sec) { sec.id = id; view.appendChild(sec); }
    state.conv = conversation(call, target, state.player, state);
    add('sec-conversation', section('Conversation', 'What was said, in order. Between a caller’s line and its answer is the wait: last sound → first audio.', state.conv.el));
    add('sec-turns', section('Turn by turn', plural((call.turns || []).length, 'answered turn') + ', from the caller’s last sound. A row opens what was said.', waterfall(call, target, state.player, state)));
    add('sec-stages', section('Where the time goes', 'This call’s median turn, stage by stage.', stageMedians(call)));
    add('sec-events', section('What else happened', 'Counted from the log, besides the answered turns.', eventsBlock(call)));
    add('sec-retro', section('Retrospective', d.retrospective && d.retrospective.updated_at ? 'Written ' + when(d.retrospective.updated_at) + '.' : null,
      d.retrospective && d.retrospective.text ? markdown(d.retrospective.text, d.repo_url) : h('p', { class: 'empty' }, 'No retrospective written yet.')));
    add('sec-notes', section('Your notes', 'How the call sounded to you: the one measurement no log has.', feedbackForm(call, d.feedback, state)));
    view.appendChild(pager('bottom'));

    if (state.player) {
      state.player.onstate = function () {
        Array.prototype.forEach.call(view.querySelectorAll('.play-turn'), function (b) {
          var on = state.player.isPlaying() && state.player.currentTurnWindow(b._turn);
          b.classList.toggle('is-playing', on);
          b.lastChild.textContent = on ? 'Pause' : 'Play this turn';
        });
      };
    }
    S.cleanup = function () { if (state.player) state.player.destroy(); S.callView = null; };
  }

  function routeCall(id, keepScroll) {
    var token = ++S.token;
    setNav('');
    var y = window.scrollY;
    if (!keepScroll) { if (S.cleanup) { S.cleanup(); S.cleanup = null; } showLoading('the call'); }
    var need = [api('/api/calls/' + encodeURIComponent(id))];
    need.push(S.ov && !keepScroll ? Promise.resolve(S.ov) : loadOverview().catch(function () { return null; }));
    return Promise.all(need).then(function (r) {
      if (token !== S.token) return;
      document.title = 'Call ' + when(r[0].call.started_at) + ' – Call journal';
      renderCall(r[0]);
      window.scrollTo(0, keepScroll ? y : 0);
    }).catch(function (e) {
      if (token !== S.token) return;
      if (e.status === 404) {
        clear(view);
        view.appendChild(h('div', { class: 'state' }, h('p', { class: 'state-h' }, 'The journal has no call with this id.'), h('p', null, h('a', { href: '#/' }, 'Back to all calls'))));
      } else showError(e, function () { routeCall(id); }, 'This call');
    });
  }

  // ====================================================================
  // Issues
  // ====================================================================

  var STATUS_ORDER = ['open', 'watching', 'decision', 'fixed', 'dropped'];

  function statusChip(status) {
    var known = STATUS_ORDER.indexOf(status) >= 0;
    return h('span', { class: 'chip s-' + (known ? status : 'other') }, glyph(known ? status : ''), status || 'unknown');
  }

  function renderIssues(data, focusId, repo) {
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    clear(view);
    var issues = (data.issues || []).slice().sort(function (a, b) { return a.id < b.id ? 1 : -1; });
    var f = S.issueFilter;
    var known = {};
    (S.ov ? S.ov.calls : []).forEach(function (c) { known[c.id] = c; });

    var counts = {}, areas = {};
    issues.forEach(function (i) { counts[i.status] = (counts[i.status] || 0) + 1; areas[i.area] = (areas[i.area] || 0) + 1; });
    var statuses = STATUS_ORDER.concat(Object.keys(counts).filter(function (k) { return STATUS_ORDER.indexOf(k) < 0; }));

    var listEl = h('div', { class: 'issues' });
    var shown = h('p', { class: 'sec-note', role: 'status' });
    var statusBar = h('div', { class: 'filters', role: 'group', 'aria-label': 'Filter by status' });
    var areaBar = h('div', { class: 'filters areas', role: 'group', 'aria-label': 'Filter by area' });

    function fbtn(bar, key, val, label, n, extra) {
      var b = h('button', { type: 'button', class: 'fchip' + (extra ? ' ' + extra : ''), 'aria-pressed': String(f[key] === val) }, label, h('span', { class: 'fchip-n num' }, n));
      b.addEventListener('click', function () { f[key] = val; paint(); });
      b._val = val;
      bar.appendChild(b);
    }
    fbtn(statusBar, 'status', 'all', 'All', issues.length);
    statuses.forEach(function (st) { fbtn(statusBar, 'status', st, [glyph(st), st], counts[st] || 0, 's-' + st); });
    fbtn(areaBar, 'area', 'all', 'All areas', issues.length);
    Object.keys(areas).sort().forEach(function (a) { fbtn(areaBar, 'area', a, a, areas[a]); });

    var rows = {};
    function row(it) {
      var body = null;
      var btn = h('button', { type: 'button', class: 'iss-line', 'aria-expanded': 'false' },
        h('span', { class: 'iss-id num' }, it.id),
        h('span', { class: 'iss-title' }, it.title),
        h('span', { class: 'iss-area' }, it.area),
        statusChip(it.status),
        h('span', { class: 'iss-date num' }, it.found || ''));
      var el = h('div', { class: 'iss', id: it.id }, btn);
      function callLink(id) {
        if (!id) return null;
        return known[id] ? h('a', { href: '#/call/' + id }, 'call of ' + when(known[id].started_at)) : h('a', { href: '#/call/' + id }, 'call ' + id);
      }
      function step(k, text, extra, missing) {
        return h('div', { class: 'step' + (text ? '' : ' none') }, h('h3', null, k), text ? inline(h('p'), text, repo) : h('p', null, missing), extra || null);
      }
      function open(on) {
        btn.setAttribute('aria-expanded', String(on));
        el.classList.toggle('open', on);
        if (on && !body) {
          var commits = (it.commits || []).length ? h('p', { class: 'step-x' }, (it.commits.length === 1 ? 'Commit ' : 'Commits '), it.commits.map(function (c, i) { return [i ? ', ' : '', commitLink(c, repo)]; })) : null;
          var foundX = (it.found_on || it.found_call) ? h('p', { class: 'step-x' }, 'Found ', it.found || '', it.found_on ? ' on: ' + it.found_on : '', it.found_call ? [' (', callLink(it.found_call), ')'] : null) : null;
          var verX = it.verified_call ? h('p', { class: 'step-x' }, 'Seen on the ', callLink(it.verified_call)) : null;
          body = h('div', { class: 'iss-body' },
            h('div', { class: 'steps' },
              step('Symptom', it.symptom, foundX, 'Not written down.'),
              step('Cause', it.cause, null, 'Not known yet.'),
              step('Fix', it.fix, commits, 'None yet.'),
              step('Verified', it.verified, verX, 'Not verified yet.')),
            it.effect ? h('p', { class: 'iss-effect' }, h('span', { class: 'iss-effect-l' }, 'Effect'), ' ', it.effect) : null);
          el.appendChild(body);
        }
        if (body) body.hidden = !on;
      }
      btn.addEventListener('click', function () {
        var on = btn.getAttribute('aria-expanded') !== 'true';
        open(on);
        history.replaceState(null, '', on ? '#/issues/' + it.id : '#/issues');
      });
      rows[it.id] = { el: el, open: open, it: it };
      return el;
    }
    issues.forEach(function (it) { listEl.appendChild(row(it)); });

    function paint() {
      var n = 0;
      issues.forEach(function (it) {
        var ok = (f.status === 'all' || it.status === f.status) && (f.area === 'all' || it.area === f.area);
        rows[it.id].el.hidden = !ok;
        if (ok) n++;
      });
      [statusBar, areaBar].forEach(function (bar, k) {
        Array.prototype.forEach.call(bar.children, function (b) { b.setAttribute('aria-pressed', String(b._val === (k ? f.area : f.status))); });
      });
      shown.textContent = n === issues.length ? 'All ' + n + ' shown, newest first.' : n + ' of ' + issues.length + ' shown, newest first.';
      empty.hidden = n > 0;
    }
    var empty = h('p', { class: 'empty' }, 'No issue matches both filters.');

    view.appendChild(h('div', { class: 'lede' }, h('h1', null, 'Issues'),
      h('p', null, 'Everything found on calls and on the bench, and what became of it: symptom, cause, fix, and whether the fix was seen to work.')));
    view.appendChild(h('div', { class: 'ledger' }, statusBar, areaBar, shown,
      issues.length ? listEl : h('p', { class: 'empty' }, 'No issues are written down yet.'), empty));

    if (focusId && rows[focusId]) {
      var r = rows[focusId];
      if (r.el.hidden || (f.status !== 'all' && r.it.status !== f.status) || (f.area !== 'all' && r.it.area !== f.area)) { f.status = 'all'; f.area = 'all'; }
      paint();
      r.open(true);
      r.el.classList.add('focus');
      requestAnimationFrame(function () { r.el.scrollIntoView({ block: 'start' }); });
    } else {
      paint();
      if (focusId) shown.textContent = 'There is no ' + focusId + ' in the ledger. ' + shown.textContent;
    }
  }

  function routeIssues(focusId) {
    var token = ++S.token;
    setNav('issues');
    document.title = 'Issues – Call journal';
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    showLoading('the issues');
    var ov = S.ov ? Promise.resolve(S.ov) : loadOverview().catch(function () { return null; });
    return Promise.all([api('/api/issues'), ov]).then(function (r) {
      if (token !== S.token) return;
      renderIssues(r[0], focusId, r[1] ? r[1].repo_url : '');
      if (!focusId) window.scrollTo(0, 0);
    }).catch(function (e) {
      if (token !== S.token) return;
      showError(e, function () { routeIssues(focusId); }, 'The issues');
    });
  }

  // ====================================================================
  // Decisions
  // ====================================================================

  function basisNode(text, repo) {
    var m = /^(measured|estimated|unknown|assumed)\b[:;,]?\s*(.*)$/i.exec(text || '');
    if (!m) return inline(h('span'), text || '', repo);
    return h('span', null, h('span', { class: 'basis-k basis-' + m[1].toLowerCase() }, m[1].toLowerCase()), m[2] ? ' ' : '', inline(h('span'), m[2], repo));
  }

  function renderDecisions(data, repo) {
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    clear(view);
    var ds = (data.decisions || []).slice().sort(function (a, b) { return a.rank - b.rank; });
    var grid = h('div', { class: 'dec', role: 'table', 'aria-label': 'Options that need a decision' });
    grid.appendChild(h('div', { class: 'dec-row dec-head', role: 'row' },
      ['', 'Option', 'What it would buy', 'What it costs', 'Puts at risk', 'Can be undone', 'Needs from you'].map(function (t) { return h('span', { role: 'columnheader' }, t); })));
    ds.forEach(function (d) {
      function cell(label, node, cls) { return h('div', { class: 'dec-c' + (cls ? ' ' + cls : ''), role: 'cell', 'data-l': label }, node); }
      grid.appendChild(h('div', { class: 'dec-row', role: 'row' },
        h('div', { class: 'dec-rank num', role: 'cell', 'aria-label': 'Rank ' + d.rank }, d.rank),
        h('div', { class: 'dec-c dec-title', role: 'cell' }, h('h3', null, d.title), d.status ? h('span', { class: 'chip d-' + String(d.status).replace(/[^a-z]/gi, '') }, d.status) : null),
        cell('What it would buy', [inline(h('p', { class: 'dec-gain' }, null), d.gain || '', repo), d.basis ? h('p', { class: 'dec-basis' }, basisNode(d.basis, repo)) : null]),
        cell('What it costs', inline(h('p'), d.cost || '–', repo)),
        cell('Puts at risk', inline(h('p'), d.guard || '–', repo)),
        cell('Can be undone', inline(h('p'), d.undo || '–', repo)),
        cell('Needs from you', inline(h('p'), d.needs || '–', repo))));
    });
    view.appendChild(h('div', { class: 'lede' }, h('h1', null, 'Decisions'),
      h('p', null, 'What is left is mostly decisions, not iterations. These are the options that need yours, ranked by what they would buy.')));
    view.appendChild(ds.length ? grid : h('p', { class: 'empty' }, 'Nothing is waiting for a decision.'));
  }

  function routeDecisions() {
    var token = ++S.token;
    setNav('decisions');
    document.title = 'Decisions – Call journal';
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    showLoading('the decisions');
    var ov = S.ov ? Promise.resolve(S.ov) : loadOverview().catch(function () { return null; });
    return Promise.all([api('/api/decisions'), ov]).then(function (r) {
      if (token !== S.token) return;
      renderDecisions(r[0], r[1] ? r[1].repo_url : '');
      window.scrollTo(0, 0);
    }).catch(function (e) {
      if (token !== S.token) return;
      showError(e, routeDecisions, 'The decisions');
    });
  }

  // ====================================================================
  // Router, refresh, keys
  // ====================================================================

  function parseRoute() {
    var p = (location.hash || '#/').replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
    if (p[0] === 'call' && p[1]) return { name: 'call', id: p[1] };
    if (p[0] === 'issues') return { name: 'issues', id: p[1] || '' };
    if (p[0] === 'decisions') return { name: 'decisions' };
    return { name: 'overview' };
  }
  function route() {
    hideTip();
    var r = parseRoute();
    if (r.name !== 'overview') stopRowPlay(); // the list's player belongs to the list
    if (r.name === 'call') return routeCall(r.id);
    if (r.name === 'issues') return routeIssues(r.id);
    if (r.name === 'decisions') return routeDecisions();
    return routeOverview(false);
  }
  window.addEventListener('hashchange', route);

  function busyCall() {
    var cv = S.callView;
    return !!(cv && ((cv.player && cv.player.isPlaying()) || cv.dirty));
  }

  // The 30 s tick: the overview is re-read; a call page is left alone.
  function autoRefresh() {
    if (document.hidden) return;
    var r = parseRoute();
    if (r.name === 'overview') routeOverview(true);
    else loadOverview().catch(markStale);
  }
  setInterval(autoRefresh, 30000);
  document.addEventListener('visibilitychange', function () {
    if (!document.hidden && S.ovAt && Date.now() - S.ovAt.getTime() > 30000) autoRefresh();
  });

  var refreshBtn = document.getElementById('refresh');
  refreshBtn.addEventListener('click', function () {
    var r = parseRoute();
    refreshBtn.disabled = true;
    var done = function () { refreshBtn.disabled = false; };
    var p;
    if (r.name === 'call') {
      if (busyCall()) p = loadOverview().catch(markStale); // a playing recording or an unsaved note is not disturbed
      else p = routeCall(r.id, true);
    } else if (r.name === 'issues') p = loadOverview().catch(markStale).then(function () { return routeIssues(r.id); });
    else if (r.name === 'decisions') p = loadOverview().catch(markStale).then(routeDecisions);
    else p = routeOverview(true);
    Promise.resolve(p).then(done, done);
  });

  document.addEventListener('keydown', function (e) {
    var cv = S.callView;
    if (!cv || !cv.player || e.altKey || e.ctrlKey || e.metaKey) return;
    var tag = e.target && e.target.tagName ? e.target.tagName.toLowerCase() : '';
    if (tag === 'input' || tag === 'textarea' || tag === 'select') return;
    if (e.key === ' ' || e.code === 'Space') {
      if (tag === 'button' || tag === 'a' || tag === 'summary') return;
      e.preventDefault(); cv.player.toggle();
    } else if (e.key === 'ArrowLeft') { e.preventDefault(); cv.player.nudge(-2); }
    else if (e.key === 'ArrowRight') { e.preventDefault(); cv.player.nudge(2); }
  });

  window.addEventListener('beforeunload', function (e) {
    if (S.callView && S.callView.dirty) { e.preventDefault(); e.returnValue = ''; }
  });

  route();
})();
