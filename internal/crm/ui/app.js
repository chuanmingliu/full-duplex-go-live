/* Lead service panel: a page over the API in internal/crm/service.go.
   The token is the one in <dir>/api.token on the box; it is kept in this
   browser only. Everything the page does, the API does; the page adds no
   rule of its own. */
(function () {
  'use strict';

  function h(tag, attrs) {
    var el = document.createElement(tag);
    if (attrs) Object.keys(attrs).forEach(function (k) {
      if (k === 'class') el.className = attrs[k];
      else if (k === 'text') el.textContent = attrs[k];
      else if (k.slice(0, 2) === 'on') el.addEventListener(k.slice(2), attrs[k]);
      else el.setAttribute(k, attrs[k]);
    });
    for (var i = 2; i < arguments.length; i++) {
      var c = arguments[i];
      if (c == null) continue;
      if (Array.isArray(c)) c.forEach(function (x) { if (x != null) el.appendChild(typeof x === 'string' ? document.createTextNode(x) : x); });
      else el.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
    }
    return el;
  }
  function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); }
  function when(iso) { if (!iso) return ''; var d = new Date(iso); return isNaN(d) ? iso : d.toLocaleString(); }
  function pretty(x) { try { return JSON.stringify(typeof x === 'string' ? JSON.parse(x) : x, null, 2); } catch (e) { return String(x); } }

  var main = document.getElementById('main');
  var tipEl = document.getElementById('tip');
  var tipTimer = 0;
  function tip(text, ms) {
    tipEl.textContent = text; tipEl.hidden = false;
    clearTimeout(tipTimer); tipTimer = setTimeout(function () { tipEl.hidden = true; }, ms || 2500);
  }

  // ----- the token -----
  var TOKEN_KEY = 'crm.token';
  function token() { try { return localStorage.getItem(TOKEN_KEY) || ''; } catch (e) { return ''; } }
  function setToken(t) { try { if (t) localStorage.setItem(TOKEN_KEY, t); else localStorage.removeItem(TOKEN_KEY); } catch (e) { /* no storage */ } }

  function api(method, path, body, raw) {
    var headers = { 'Authorization': 'Bearer ' + token(), 'X-Operator': 'panel' };
    var opts = { method: method, headers: headers };
    if (body !== undefined) {
      if (raw) { headers['Content-Type'] = 'text/plain; charset=utf-8'; opts.body = body; }
      else { headers['Content-Type'] = 'application/json'; opts.body = typeof body === 'string' ? body : JSON.stringify(body); }
    }
    return fetch(path, opts).then(function (r) {
      var ct = r.headers.get('content-type') || '';
      var p = ct.indexOf('application/json') >= 0 ? r.json() : r.text();
      return p.then(function (d) {
        if (r.status === 401) { var e = new Error('the token was not accepted'); e.status = 401; throw e; }
        if (!r.ok) { var m = (d && d.error) || (typeof d === 'string' ? d : r.statusText); var err = new Error(m || ('HTTP ' + r.status)); err.status = r.status; throw err; }
        return d;
      });
    });
  }

  function askToken(reason) {
    clear(main);
    var input = h('input', { type: 'text', autocomplete: 'off', spellcheck: 'false', placeholder: 'paste the token' });
    input.value = token();
    var msg = h('div', { class: 'msg err', hidden: '' });
    if (reason) { msg.textContent = reason; msg.hidden = false; }
    var form = h('form', { class: 'card gate', onsubmit: function (e) {
      e.preventDefault();
      setToken(input.value.trim());
      api('GET', '/api/templates').then(function () { tip('Token accepted'); route(); })
        .catch(function (err) { msg.textContent = err.message; msg.hidden = false; });
    } },
      h('h1', { text: 'The lead service needs its token' }),
      h('p', { class: 'sub' }, 'It is the line in ', h('code', { class: 'mono', text: 'api.token' }), ' in the service’s data directory on the box (', h('code', { class: 'mono', text: 'cat ~/golive-crm/api.token' }), '). It stays in this browser.'),
      msg, h('label', { text: 'Token' }), input,
      h('div', { class: 'row' }, h('button', { class: 'btn btn-primary', type: 'submit', text: 'Use it' })));
    main.appendChild(form);
    input.focus();
  }
  document.getElementById('token-btn').addEventListener('click', function () { askToken(''); });

  function guard(p) {
    return p.catch(function (err) {
      if (err.status === 401) { askToken('The token was not accepted; paste the current one.'); }
      throw err;
    });
  }
  function errBox(err) { return h('div', { class: 'msg err', text: err.message || String(err) }); }
  function setNav(name) {
    document.querySelectorAll('.nav a').forEach(function (a) { if (a.dataset.nav === name) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
  }
  function page(title, sub) {
    clear(main);
    main.appendChild(h('h1', { text: title }));
    if (sub) main.appendChild(h('p', { class: 'sub' }, sub));
  }

  // ----- health in the top bar -----
  function health() {
    fetch('/healthz').then(function (r) { return r.json(); }).then(function (d) {
      document.getElementById('top-health').textContent = d.templates + ' templates · ' + d.events + ' events in the log';
    }).catch(function () { document.getElementById('top-health').textContent = 'the service is not answering'; });
  }

  // ----- templates -----
  function templates(name) {
    setNav('templates');
    page('Templates', 'The prompts, in versions. Saving makes a new version; nothing is ever overwritten. A template is Jinja2 and is checked when saved.');
    var grid = h('div', { class: 'grid' });
    var list = h('div', { class: 'list' });
    var pane = h('div');
    grid.appendChild(h('div', null, list, h('div', { class: 'row' }, h('button', { class: 'btn', text: 'New template', onclick: function () { showTemplate(pane, null, []); } }))));
    grid.appendChild(pane);
    main.appendChild(grid);
    guard(api('GET', '/api/templates')).then(function (d) {
      var ts = d.templates || [];
      if (!ts.length) list.appendChild(h('div', { class: 'muted', style: 'padding:10px 12px', text: 'No templates yet.' }));
      ts.forEach(function (t) {
        list.appendChild(h('button', { type: 'button', 'aria-current': String(t.name === name), onclick: function () { location.hash = '#/templates/' + encodeURIComponent(t.name); } },
          t.name, h('span', { class: 'meta' }, 'v' + t.version + ' · ' + t.chars + ' chars · ' + (t.names || []).length + ' variables')));
      });
      if (name) loadTemplate(pane, name, 0);
      else if (ts.length) location.hash = '#/templates/' + encodeURIComponent(ts[0].name);
    }).catch(function (err) { if (err.status !== 401) pane.appendChild(errBox(err)); });
  }
  function loadTemplate(pane, name, version) {
    clear(pane); pane.appendChild(h('p', { class: 'muted', text: 'Loading…' }));
    guard(api('GET', '/api/templates/' + encodeURIComponent(name) + (version ? '?version=' + version : ''))).then(function (d) {
      showTemplate(pane, d.template, d.versions || []);
    }).catch(function (err) { if (err.status !== 401) { clear(pane); pane.appendChild(errBox(err)); } });
  }
  function showTemplate(pane, t, versions) {
    clear(pane);
    var isNew = !t;
    var nameIn = h('input', { type: 'text', placeholder: 'name, e.g. car-renewal' });
    var body = h('textarea', { class: 'tall', spellcheck: 'false' });
    var note = h('input', { type: 'text', placeholder: 'what changed (optional)' });
    var msg = h('div');
    if (t) { nameIn.value = t.name; nameIn.readOnly = true; body.value = t.body || ''; }
    var verSel = h('select', { onchange: function () { loadTemplate(pane, t.name, +verSel.value); } });
    versions.slice().reverse().forEach(function (v) {
      verSel.appendChild(h('option', { value: v.version, text: 'v' + v.version + ' · ' + v.sha + ' · ' + when(v.created_at) + (v.note ? ' · ' + v.note : '') }));
    });
    if (t) verSel.value = String(t.version);
    var head = h('div', { class: 'card' },
      h('div', { class: 'row' }, h('strong', { text: isNew ? 'New template' : t.name }), t ? h('span', { class: 'chip', text: 'v' + t.version }) : null, t ? h('span', { class: 'chip mono', text: t.sha }) : null),
      t ? h('dl', { class: 'kv' },
        h('dt', { text: 'Variables' }), h('dd', { class: 'mono', text: (t.names || []).join(', ') || '—' }),
        h('dt', { text: 'Static prefix' }), h('dd', { text: t.static_chars + ' of ' + t.chars + ' characters before the first variable (what the model’s prompt cache keeps between leads)' }),
        h('dt', { text: 'Versions' }), h('dd', null, verSel)) : null);
    var edit = h('div', { class: 'card' },
      isNew ? h('label', { text: 'Name' }) : null, isNew ? nameIn : null,
      h('label', { text: 'Template (Jinja2)' }), body,
      h('label', { text: 'Note' }), note,
      h('div', { class: 'row' },
        h('button', { class: 'btn btn-primary', type: 'button', text: isNew ? 'Save v1' : 'Save as v' + (t.version + 1), onclick: function () {
          var name = isNew ? nameIn.value.trim() : t.name;
          if (!name) { msg.replaceChildren(errBox(new Error('a name is needed'))); return; }
          guard(api('PUT', '/api/templates/' + encodeURIComponent(name) + '?note=' + encodeURIComponent(note.value), { body: body.value, note: note.value })).then(function (d) {
            tip('Saved ' + d.template.name + ' v' + d.template.version);
            location.hash = '#/templates/' + encodeURIComponent(name);
            templates(name);
          }).catch(function (err) { if (err.status !== 401) msg.replaceChildren(errBox(err)); });
        } }),
        t ? h('a', { class: 'btn', href: '#/render?template=' + encodeURIComponent(t.name), text: 'Render it' }) : null),
      msg);
    pane.appendChild(head); pane.appendChild(edit);
  }

  // ----- configs -----
  var CONFIG_HELP = {
    'quote-rules': 'Placeholder rules for a quote when no business service is configured: coverages with base premiums, discounts, a simulated delay. See internal/crm/quote.go.',
    'quote-plans': 'What a quotation needs before it runs. Named plans; "default" picks one, a call can name another in its working memory (quote_plan — a lead’s own plan, once leads exist). A plan lists its coverages and its fields: name, "for" (the coverages that need it, ["*"] for all), "question" (what the assistant asks when it is unknown), "if_unknown" (skip the coverages or stop), "source" (rules, api or any — the business service looks a car up by its plate and does not need the price). With a must field missing the tool answers status=need and the assistant asks one question at a time; only with every field known is a quotation made. See internal/crm/plans.go.',
    'hangup-rules': 'When the assistant ends a call. Each rule is a case: kind "model" (the chat model judges "when" and says "farewell" before hanging up — add a case by adding a line), "keyword" (a regular expression on the caller’s words; the engine says the farewell and hangs up), "silence" (no word from the caller for "seconds"), "duration" (the call has run "seconds"). "enabled": false keeps a case without using it. Read by golive at the start of every call; a bad pattern is refused on save. See internal/tools/hangup.go.',
    'quote-api': 'The business’s quotation service: base_url up to and including /intranet, the channel ids, the coverage codes, the phone and vehicle variables and their defaults, and how long to poll. With a base_url set, quote_total goes here. See internal/crm/quoteapi.go.'
  };
  function configs(name) {
    setNav('configs');
    page('Configs', 'Business configuration the service reads, in versions, as JSON. The assistant’s quotation tool reads quote-api (the business service) or quote-rules (the placeholder); hangup-rules says when a call ends; quote-plans says what a quotation must know first.');
    var grid = h('div', { class: 'grid' });
    var list = h('div', { class: 'list' });
    var pane = h('div');
    grid.appendChild(h('div', null, list, h('div', { class: 'row' }, h('button', { class: 'btn', text: 'New config', onclick: function () { showConfig(pane, null); } }))));
    grid.appendChild(pane);
    main.appendChild(grid);
    guard(api('GET', '/api/configs')).then(function (d) {
      var cs = d.configs || [];
      cs.forEach(function (c) {
        list.appendChild(h('button', { type: 'button', 'aria-current': String(c.name === name), onclick: function () { location.hash = '#/configs/' + encodeURIComponent(c.name); } },
          c.name, h('span', { class: 'meta' }, 'v' + c.version + ' · ' + c.chars + ' chars · ' + when(c.created_at))));
      });
      if (name) loadConfig(pane, name, 0);
      else if (cs.length) location.hash = '#/configs/' + encodeURIComponent(cs[0].name);
    }).catch(function (err) { if (err.status !== 401) pane.appendChild(errBox(err)); });
  }
  function loadConfig(pane, name, version) {
    clear(pane); pane.appendChild(h('p', { class: 'muted', text: 'Loading…' }));
    guard(api('GET', '/api/configs/' + encodeURIComponent(name) + (version ? '?version=' + version : ''))).then(function (d) {
      showConfig(pane, d.config);
    }).catch(function (err) { if (err.status !== 401) { clear(pane); pane.appendChild(errBox(err)); } });
  }
  function showConfig(pane, c) {
    clear(pane);
    var isNew = !c;
    var nameIn = h('input', { type: 'text', placeholder: 'name' });
    var body = h('textarea', { class: 'tall', spellcheck: 'false' });
    var note = h('input', { type: 'text', placeholder: 'what changed (optional)' });
    var msg = h('div');
    if (c) { nameIn.value = c.name; nameIn.readOnly = true; body.value = pretty(c.body); }
    var verIn = h('input', { type: 'number', class: 'short', min: '1', value: c ? c.version : 1 });
    pane.appendChild(h('div', { class: 'card' },
      h('div', { class: 'row' }, h('strong', { text: isNew ? 'New config' : c.name }), c ? h('span', { class: 'chip', text: 'v' + c.version }) : null, c ? h('span', { class: 'chip mono', text: c.sha }) : null, c ? h('span', { class: 'muted', text: when(c.created_at) + ' by ' + c.by + (c.note ? ' · ' + c.note : '') }) : null),
      c && CONFIG_HELP[c.name] ? h('p', { class: 'muted', text: CONFIG_HELP[c.name] }) : null,
      c ? h('div', { class: 'row' }, h('span', { class: 'muted', text: 'Open another version:' }), verIn, h('button', { class: 'btn', type: 'button', text: 'Open', onclick: function () { loadConfig(pane, c.name, +verIn.value); } })) : null));
    pane.appendChild(h('div', { class: 'card' },
      isNew ? h('label', { text: 'Name' }) : null, isNew ? nameIn : null,
      h('label', { text: 'JSON' }), body,
      h('label', { text: 'Note' }), note,
      h('div', { class: 'row' },
        h('button', { class: 'btn', type: 'button', text: 'Tidy', onclick: function () { try { body.value = pretty(body.value); } catch (e) { /* leave as is */ } } }),
        h('button', { class: 'btn btn-primary', type: 'button', text: isNew ? 'Save v1' : 'Save as v' + (c.version + 1), onclick: function () {
          var name = isNew ? nameIn.value.trim() : c.name;
          if (!name) { msg.replaceChildren(errBox(new Error('a name is needed'))); return; }
          var parsed;
          try { parsed = JSON.parse(body.value); } catch (e) { msg.replaceChildren(errBox(new Error('not JSON: ' + e.message))); return; }
          guard(api('PUT', '/api/configs/' + encodeURIComponent(name) + '?note=' + encodeURIComponent(note.value), parsed)).then(function (d) {
            tip('Saved ' + d.config.name + ' v' + d.config.version);
            location.hash = '#/configs/' + encodeURIComponent(name);
            configs(name);
          }).catch(function (err) { if (err.status !== 401) msg.replaceChildren(errBox(err)); });
        } })),
      msg));
  }

  // ----- render & call -----
  function render(params) {
    setNav('render');
    page('Render & call', 'Render a template with a lead’s variables, read the prompt, then place the call on it. The variables’ values go into the rendered prompt, which is kept on the box; they are not logged anywhere else.');
    var tplSel = h('select');
    var verIn = h('input', { type: 'number', class: 'short', min: '0', value: '0', title: '0 is the latest' });
    var vars = h('textarea', { spellcheck: 'false' });
    vars.value = params.vars || '{\n  \n}';
    var greet = h('input', { type: 'text', placeholder: 'leave empty for the template’s own (the variable start), or the service’s default' });
    var lead = h('input', { type: 'text', placeholder: 'optional: a lead id for the record' });
    var out = h('div');
    var greetingCard = h('div', { class: 'card' }, h('strong', { text: 'Default opening line' }), h('p', { class: 'muted', text: 'Used when a call is placed without one. Loading…' }));
    main.appendChild(h('div', { class: 'card' },
      h('label', { text: 'Template' }), h('div', { class: 'row' }, tplSel, h('span', { class: 'muted', text: 'version' }), verIn),
      h('label', { text: 'Variables (JSON object)' }), vars,
      h('label', { text: 'Opening line' }), greet,
      h('label', { text: 'Lead' }), lead,
      h('div', { class: 'row' },
        h('button', { class: 'btn btn-primary', type: 'button', text: 'Render', onclick: function () { go(false, true); } }),
        h('button', { class: 'btn', type: 'button', text: 'Dry run (everything but the dial)', onclick: function () { go(true, false); } }),
        h('button', { class: 'btn btn-danger', type: 'button', text: 'Place the call', onclick: function () {
          if (window.confirm('Dial the locked number now, on this prompt?')) go(false, false);
        } }))));
    main.appendChild(out);
    main.appendChild(greetingCard);
    guard(api('GET', '/api/templates')).then(function (d) {
      (d.templates || []).forEach(function (t) { tplSel.appendChild(h('option', { value: t.name, text: t.name + ' (v' + t.version + ', ' + (t.names || []).length + ' variables)' })); });
      if (params.template) tplSel.value = params.template;
      if (!params.vars && tplSel.value) seedVars(d.templates, tplSel.value);
      tplSel.addEventListener('change', function () { if (vars.value.trim() === '' || vars.value.trim() === '{}' || vars.dataset.seeded === '1') seedVars(d.templates, tplSel.value); });
    }).catch(function () {});
    function seedVars(ts, name) {
      var t = ts.filter(function (x) { return x.name === name; })[0];
      if (!t) return;
      var o = {}; (t.names || []).forEach(function (n) { o[n] = ''; });
      vars.value = JSON.stringify(o, null, 2); vars.dataset.seeded = '1';
    }
    vars.addEventListener('input', function () { vars.dataset.seeded = '0'; });
    guard(api('GET', '/api/greeting')).then(function (d) {
      clear(greetingCard);
      var g = h('input', { type: 'text' }); g.value = d.greeting || '';
      greetingCard.appendChild(h('strong', { text: 'Default opening line' }));
      greetingCard.appendChild(h('p', { class: 'muted', text: 'Used when a call is placed without one.' }));
      greetingCard.appendChild(g);
      greetingCard.appendChild(h('div', { class: 'row' }, h('button', { class: 'btn', type: 'button', text: 'Save', onclick: function () {
        guard(api('PUT', '/api/greeting', { greeting: g.value })).then(function () { tip('Opening line saved'); }).catch(function (err) { if (err.status !== 401) tip(err.message, 5000); });
      } })));
    }).catch(function () {});
    function go(dry, renderOnly) {
      var v;
      try { v = JSON.parse(vars.value || '{}'); } catch (e) { out.replaceChildren(errBox(new Error('variables are not JSON: ' + e.message))); return; }
      var req = { template: tplSel.value, version: +verIn.value || 0, vars: v, greeting: greet.value, lead: lead.value, dry_run: dry };
      out.replaceChildren(h('p', { class: 'muted', text: renderOnly ? 'Rendering…' : dry ? 'Checking…' : 'Dialling…' }));
      var p = renderOnly ? api('POST', '/api/render', req) : api('POST', '/api/call', req);
      guard(p).then(function (d) {
        clear(out);
        if (renderOnly) {
          out.appendChild(h('div', { class: 'card' },
            h('div', { class: 'row' }, h('strong', { text: 'Rendered' }), h('span', { class: 'chip', text: d.chars + ' chars' }), h('span', { class: 'chip mono', text: d.sha }),
              (d.undefined || []).length ? h('span', { class: 'chip warn', text: 'undefined: ' + d.undefined.join(', ') }) : h('span', { class: 'chip good', text: 'every variable supplied' }),
              (d.unused || []).length ? h('span', { class: 'chip warn', text: 'unused: ' + d.unused.join(', ') }) : null),
            h('pre', { class: 'pre', text: d.instructions })));
        } else {
          out.appendChild(h('div', { class: 'card' },
            h('div', { class: 'row' }, h('strong', { text: dry ? 'Dry run' : 'Call placed' }), h('span', { class: 'chip ' + (d.ok ? 'good' : 'bad'), text: d.ok ? 'ok' : 'not ok' }), d.call_id ? h('span', { class: 'chip mono', text: d.call_id }) : null),
            h('pre', { class: 'pre', text: pretty(d) })));
        }
      }).catch(function (err) { if (err.status !== 401) out.replaceChildren(errBox(err)); });
    }
  }

  // ----- quote -----
  function quote() {
    setNav('quote');
    page('Quote', 'Ask the quotation tool what the assistant would be told. The request is what golive sends for quote_total: the plan under memory.quote, the lead’s variables beside it.');
    var body = h('textarea', { spellcheck: 'false' });
    body.value = JSON.stringify({ tool: 'quote_total', session: 'panel', arguments: {}, memory: { carNum: '浙A12345', vehiclePrice: 150000, seats: 5, quote: { '交强险': true, '车损险': true, '三者险': '200万', '座位险': true } }, memory_version: 1 }, null, 2);
    var out = h('div');
    main.appendChild(h('div', { class: 'card' }, h('label', { text: 'Request (JSON)' }), body,
      h('div', { class: 'row' }, h('button', { class: 'btn btn-primary', type: 'button', text: 'Quote', onclick: function () {
        var v; try { v = JSON.parse(body.value); } catch (e) { out.replaceChildren(errBox(new Error('not JSON: ' + e.message))); return; }
        out.replaceChildren(h('p', { class: 'muted', text: 'Asking… (the business service can take a while)' }));
        var t0 = Date.now();
        guard(api('POST', '/api/tools/quote', v)).then(function (d) {
          out.replaceChildren(h('div', { class: 'card' },
            h('div', { class: 'row' }, h('strong', { text: 'Result' }), h('span', { class: 'chip', text: (Date.now() - t0) + ' ms' }), d.source ? h('span', { class: 'chip', text: d.source }) : null, d.total != null ? h('span', { class: 'chip good', text: 'total ' + d.total }) : null),
            h('pre', { class: 'pre', text: pretty(d) })));
        }).catch(function (err) { if (err.status !== 401) out.replaceChildren(errBox(err)); });
      } }))));
    main.appendChild(out);
  }

  // ----- calls -----
  function calls() {
    setNav('calls');
    page('Calls', 'Every call placed through the service, newest first: which template and version, the exact prompt it ran on, and the journal’s name for it.');
    var host = h('div', { class: 'card' }, h('p', { class: 'muted', text: 'Loading…' }));
    main.appendChild(host);
    var viewer = h('div');
    main.appendChild(viewer);
    guard(api('GET', '/api/calls?n=200')).then(function (d) {
      clear(host);
      var cs = d.calls || [];
      if (!cs.length) { host.appendChild(h('p', { class: 'muted', text: 'No calls yet.' })); return; }
      var tb = h('tbody');
      cs.forEach(function (c) {
        tb.appendChild(h('tr', null,
          h('td', { text: when(c.at) }),
          h('td', null, c.template + ' ', h('span', { class: 'chip', text: 'v' + c.version })),
          h('td', { class: 'n', text: c.chars }),
          h('td', null, (c.vars || []).join(', '), (c.undefined || []).length ? h('span', { class: 'chip warn', text: 'undefined: ' + c.undefined.join(', ') }) : null),
          h('td', { text: c.lead || '' }),
          h('td', { class: 'mono', text: c.call_id || '' }),
          h('td', null, h('button', { class: 'btn', type: 'button', text: 'Prompt', onclick: function () {
            viewer.replaceChildren(h('p', { class: 'muted', text: 'Loading…' }));
            guard(api('GET', '/api/renders/' + encodeURIComponent(c.render))).then(function (text) {
              viewer.replaceChildren(h('div', { class: 'card' }, h('div', { class: 'row' }, h('strong', { text: 'The prompt the call ran on' }), h('span', { class: 'chip mono', text: c.render })), h('pre', { class: 'pre', text: text })));
              viewer.scrollIntoView({ behavior: 'smooth' });
            }).catch(function (err) { if (err.status !== 401) viewer.replaceChildren(errBox(err)); });
          } }), c.call_id ? h('a', { class: 'btn btn-quiet', href: 'http://' + location.hostname + ':8790/#/calls/' + encodeURIComponent(c.call_id), target: '_blank', rel: 'noopener', text: 'Journal' }) : null)));
      });
      host.appendChild(h('table', null, h('thead', null, h('tr', null, h('th', { text: 'When' }), h('th', { text: 'Template' }), h('th', { class: 'n', text: 'Chars' }), h('th', { text: 'Variables' }), h('th', { text: 'Lead' }), h('th', { text: 'Call' }), h('th'))), tb));
    }).catch(function (err) { if (err.status !== 401) host.replaceChildren(errBox(err)); });
  }

  // ----- routing -----
  function route() {
    health();
    if (!token()) { askToken(''); return; }
    var hash = location.hash.replace(/^#\/?/, '');
    var q = {}, qi = hash.indexOf('?');
    if (qi >= 0) { hash.slice(qi + 1).split('&').forEach(function (kv) { var p = kv.split('='); q[decodeURIComponent(p[0])] = decodeURIComponent(p[1] || ''); }); hash = hash.slice(0, qi); }
    var parts = hash.split('/').map(decodeURIComponent);
    switch (parts[0]) {
      case '': case 'templates': templates(parts[1] || ''); break;
      case 'configs': configs(parts[1] || ''); break;
      case 'render': render(q); break;
      case 'quote': quote(); break;
      case 'calls': calls(); break;
      default: templates('');
    }
  }
  window.addEventListener('hashchange', route);
  route();
})();
