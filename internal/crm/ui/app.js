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
    document.getElementById('skills-nav').hidden = true;
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

  var businessPurposes = ['follow-up', 'reminder', 'survey'];

  function businessContent(pane) {
    clear(pane);
    var card = h('section', { class: 'card' }, h('h2', { text: 'Business content' }),
      h('p', { text: 'Scheduled calls use the instructions saved for their purpose. Leave a field empty to use the standard call prompt.' }));
    pane.appendChild(card);
    businessPurposes.forEach(function (purpose) {
      var path = '/api/call-purposes/' + purpose;
      var field = h('textarea', { rows: 4, disabled: '', placeholder: 'What should the assistant explain or ask?' });
      var msg = h('div', { role: 'status' });
      var save = h('button', { class: 'btn', type: 'button', disabled: '', text: 'Save ' + purpose,
        onclick: function () {
          save.disabled = field.disabled = true;
          guard(api('PUT', path, { content: field.value })).then(function (result) {
            field.value = result.content;
            msg.textContent = 'Saved.';
          }).catch(function (err) { if (err.status !== 401) msg.replaceChildren(errBox(err)); })
            .finally(function () { save.disabled = field.disabled = false; });
        } });
      card.appendChild(h('div', null, h('label', null, purpose, field), save, msg));
      guard(api('GET', path)).then(function (result) {
        field.value = result.content;
        save.disabled = field.disabled = false;
      }).catch(function (err) { if (err.status !== 401) msg.replaceChildren(errBox(err)); });
    });
  }

  function uploadLeads(pane) {
    var file = h('input', { type: 'file', accept: '.csv,text/csv', required: '' });
    var msg = h('div', { role: 'status' });
    var submit = h('button', { type: 'submit', class: 'btn btn-primary', text: 'Import leads' });
    pane.replaceChildren(h('form', { class: 'card', onsubmit: function (event) {
      event.preventDefault();
      var selected = file.files[0];
      if (!selected) return;
      if (selected.size > 2 * 1024 * 1024) { msg.textContent = 'CSV exceeds 2 MiB.'; return; }
      submit.disabled = true;
      msg.textContent = 'Importing…';
      guard(api('POST', '/api/leads/import', selected, true)).then(function (d) {
        tip('Imported ' + d.created + ' · Duplicates skipped ' + d.skipped + ' · Registrations queued ' + d.registration_queued, 8000);
        if (pane.isConnected) route();
      }).catch(function (err) { if (err.status !== 401) msg.replaceChildren(errBox(err)); })
        .finally(function () { submit.disabled = false; });
    } }, h('h2', { text: 'Import leads from CSV' }),
    h('p', null, 'Use a UTF-8 CSV with name and phone. Replace the example row in the ',
      h('a', { href: 'lead-template.csv', download: 'lead-template.csv', text: 'CSV template' }), '.'),
    h('p', { text: 'Optional columns are purpose, call_time, repeat_days and outbound_consent. Use 24-hour Shanghai time and repeat days such as Mon|Wed|Fri. Consent defaults to false.' }),
    h('p', { text: 'Existing phone numbers are skipped. Up to 1,000 rows and 2 MiB per upload.' }),
    h('label', null, 'CSV file', file), submit, msg));
  }

  function schedule(tag) {
    setNav('schedule');
    page('Schedule');
    var list = h('div', { class: 'list' });
    var pane = h('div');
    var filter = h('select', { onchange: function () {
      location.hash = '#/schedule' + (filter.value ? '?tag=' + encodeURIComponent(filter.value) : '');
    } }, h('option', { value: '', text: 'All leads' }));
    var add = h('button', { class: 'btn', text: 'New lead', disabled: '' });
    main.appendChild(h('div', { class: 'grid' }, h('div', null,
      h('label', null, 'Filter by tag', filter), list,
      add, h('button', { class: 'btn', type: 'button', text: 'Upload CSV', onclick: function () { uploadLeads(pane); } }),
      h('button', { class: 'btn', type: 'button', text: 'Business content', onclick: function () { businessContent(pane); } })), pane));
    guard(api('GET', '/api/leads' + (tag ? '?tag=' + encodeURIComponent(tag) : ''))).then(function (d) {
      filter.appendChild(h('option', { value: d.outbound_tag, text: d.outbound_tag }));
      filter.appendChild(h('option', { value: d.schedule_tag, text: d.schedule_tag }));
      filter.value = tag;
      add.onclick = function () { editLead(pane, d.outbound_tag, d.schedule_tag, d.schedule_timezone); };
      add.disabled = false;
      d.leads.forEach(function (lead) {
        list.appendChild(h('button', { type: 'button', onclick: function () { editLead(pane, d.outbound_tag, d.schedule_tag, d.schedule_timezone, lead); } },
          lead.values.name, h('span', { class: 'meta', text: (lead.tags || []).join(', ') }),
          lead.call_plan ? h('span', { class: 'meta', text: planSummary(lead.call_plan) }) : null));
      });
      if (!d.leads.length) list.textContent = 'No matching leads.';
    }).catch(function (err) { if (err.status !== 401) list.replaceChildren(errBox(err)); });
  }

  var dayNames = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

  function zoneLabel(zone) {
    return zone === 'Asia/Shanghai' ? 'Shanghai, UTC+8' : zone;
  }

  function planSummary(plan) {
    var days = plan.days.length === 7 ? 'Every day' : plan.days.map(function (day) { return dayNames[day]; }).join(', ');
    return days + ' at ' + plan.time + ' (' + zoneLabel(plan.timezone) + ')';
  }

  function callPlanFields(toggle, plan, timezone) {
    var time = h('input', { type: 'time', value: plan ? plan.time : '' });
    var weekdayInputs = [];
    var days = h('div', { class: 'row' });
    [1, 2, 3, 4, 5, 6, 0].forEach(function (day) {
      var box = h('input', { type: 'checkbox' });
      box.checked = plan ? plan.days.includes(day) : day > 0 && day < 6;
      weekdayInputs[day] = box;
      days.appendChild(h('label', null, box, ' ' + dayNames[day]));
    });
    var field = h('fieldset', null, h('legend', { text: 'Outbound call time' }),
      h('label', null, 'Local time (' + zoneLabel(timezone) + ')', time),
      h('fieldset', null, h('legend', { text: 'Repeat on' }), days),
      h('button', { type: 'button', class: 'btn', text: 'Clear time', onclick: function () { time.value = ''; refresh(); } }));
    function selectedDays() {
      return [0, 1, 2, 3, 4, 5, 6].filter(function (day) { return weekdayInputs[day].checked; });
    }
    function refresh() {
      field.hidden = field.disabled = !toggle.checked;
      time.setCustomValidity(toggle.checked && time.value && !selectedDays().length ? 'Choose at least one repeat day.' : '');
    }
    field.addEventListener('input', refresh);
    toggle.addEventListener('change', refresh);
    refresh();
    return { field: field, read: function () {
      return toggle.checked && time.value ? { time: time.value, days: selectedDays() } : {};
    } };
  }

  function editLead(pane, outboundTag, scheduleTag, timezone, lead) {
    var values = lead ? lead.values : {};
    var id = lead ? lead.id : Array.from(crypto.getRandomValues(new Uint8Array(16)), function (n) {
      return n.toString(16).padStart(2, '0');
    }).join('');
    var name = h('input', { type: 'text', required: '', value: values.name || '' });
    var phone = h('input', { type: 'text', inputmode: 'tel', required: '', value: values.phone || '' });
    var purpose = h('select', null, h('option', { value: 'normal', text: 'Normal call' }), businessPurposes.map(function (v) {
      return h('option', { value: v, text: v });
    }));
    purpose.value = values.purpose || 'follow-up';
    var tagged = h('input', { type: 'checkbox' });
    tagged.checked = !lead || (lead.tags || []).includes(outboundTag);
    var scheduleBox = h('input', { type: 'checkbox' });
    scheduleBox.checked = !!(lead && (lead.tags || []).includes(scheduleTag));
    var plan = callPlanFields(scheduleBox, lead && lead.call_plan, timezone);
    var consent = h('input', { type: 'checkbox' });
    consent.checked = !!(lead && lead.outbound_consent);
    var save = h('button', { class: 'btn btn-primary', type: 'submit', text: 'Save lead' });
    var msg = h('div');
    pane.replaceChildren(h('form', { class: 'card', onsubmit: function (event) {
      event.preventDefault();
      save.disabled = true;
      guard(api('PUT', '/api/leads/' + encodeURIComponent(id), {
        version: lead ? lead.version : 0, name: name.value, phone: phone.value,
        purpose: purpose.value, tagged: tagged.checked, schedule: scheduleBox.checked, call_plan: plan.read(), outbound_consent: consent.checked
      })).then(function (result) {
        tip(result.registration_queued ? 'Lead saved. Registration queued.' : 'Lead saved');
        if (pane.isConnected) route();
      }).catch(function (err) {
        save.disabled = err.status === 409;
        if (err.status !== 401) msg.replaceChildren(errBox(err));
        if (err.status === 409) msg.appendChild(h('button', { class: 'btn', type: 'button', text: 'Reload leads', onclick: route }));
      });
    } }, h('h2', { text: lead ? 'Edit lead' : 'New lead' }),
    h('label', null, 'Name', name), h('label', null, 'Phone number', phone),
    h('label', null, 'Call purpose', purpose), h('label', null, tagged, ' ' + outboundTag),
    h('label', null, scheduleBox, ' ' + scheduleTag), plan.field,
    h('label', null, consent, ' Agreed to outbound calls'), save, msg));

  }

  function transcriptLabelForm(lead) {
    var transcript = h('input', { type: 'text', required: '', pattern: '[A-Za-z0-9_\\-]{1,64}' });
    var vehicle = h('input', { type: 'text', maxlength: '128' });
    var turns = h('textarea', { required: '', rows: '6', placeholder: '[{"role":"user","content":"Please call me after five."}]' });
    var previous = h('input', { type: 'number', min: '0', value: '0' });
    var output = h('div', { role: 'status' });
    var submit = h('button', { class: 'btn', type: 'submit', text: 'Label and save' });
    var form = h('form', { onsubmit: function (event) {
      event.preventDefault();
      var messages;
      try { messages = JSON.parse(turns.value); }
      catch (err) { output.textContent = 'Enter a JSON array of chat turns.'; return; }
      submit.disabled = true;
      output.textContent = 'Labeling transcript…';
      guard(api('POST', '/api/leads/' + encodeURIComponent(lead.id) + '/labels', {
        transcript_id: transcript.value.trim(), vehicle_id: vehicle.value.trim(),
        turns: messages, previous_seq: Number(previous.value)
      })).then(function (revision) {
        previous.value = revision.seq;
        output.replaceChildren(h('p', { text: 'Saved memory revision ' + revision.seq }),
          h('pre', { text: pretty(revision.value) }), h('details', null,
            h('summary', { text: 'Source evidence' }), h('pre', { text: pretty(revision.labeling) })));
      }).catch(function (err) { if (err.status !== 401) output.replaceChildren(errBox(err)); })
        .finally(function () { submit.disabled = false; });
    } }, h('label', null, 'Transcript ID', transcript), h('label', null, 'Vehicle ID (optional)', vehicle),
    h('label', null, 'Chat turns (JSON)', turns), h('label', null, 'Previous memory revision (0 for new)', previous),
    submit, output);
    transcript.addEventListener('input', function () { previous.value = '0'; });
    return h('div', null, form);
  }

  function labelResultsPanel() {
    var root = h('section', { class: 'card label-results' });
    var query = h('input', { type: 'search', placeholder: 'Call, transcript, lead, vehicle or revision ID', 'aria-label': 'Search label results by ID' });
    var list = h('div');
    var message = h('div', { role: 'status' });
    var more = h('button', { class: 'btn', type: 'button', text: 'Load older results', hidden: '' });
    var filter = '', cursor = 0, generation = 0, expandedPage = false;
    function row(result) {
      var content = h('div');
      var detail = h('details', null, h('summary', null,
        h('strong', { text: result.call_id || result.transcript_id }), ' · ' + (result.name || result.lead_id),
        h('span', { class: 'muted', text: ' · ' + when(result.at) + ' · #' + result.revision +
          ' · ' + (result.by === 'hangup' ? 'Hangup' : 'Manual') + ' · ' + result.observations + ' labels' +
          (result.current ? '' : ' · Historical revision') })), content);
      var loaded = false;
      detail.ontoggle = function () {
        if (!detail.open || loaded) return;
        loaded = true;
        content.textContent = 'Loading result…';
        guard(api('GET', '/api/label-results/' + result.revision)).then(function (data) {
          var receipt = data.labeling;
          content.replaceChildren(h('p', { text: 'Lead ' + result.lead_id + ' · Transcript ' + result.transcript_id +
            (result.vehicle_id ? ' · Vehicle ' + result.vehicle_id : '') + ' · OpenViking ' + result.archive }),
            h('pre', { class: 'pre', text: pretty(data.fields) }),
            h('details', null, h('summary', { text: 'Source evidence and user info' }),
              h('pre', { class: 'pre', text: pretty({user:receipt.user, model:receipt.model, schema_version:receipt.schema_version, observations:receipt.observations}) })));
          if (receipt.original_session) content.appendChild(h('details', null, h('summary', { text: 'Original session' }),
            h('pre', { class: 'pre', text: pretty(receipt.original_session) })));
        }).catch(function (err) { loaded = false; if (err.status !== 401) content.replaceChildren(errBox(err)); });
      };
      return detail;
    }
    async function load(append) {
      var ticket = ++generation;
      more.disabled = true;
      try {
        var params = new URLSearchParams({id:filter});
        if (append) params.set('before', cursor);
        var data = await guard(api('GET', '/api/label-results?' + params));
        if (ticket !== generation || !root.isConnected) return;
        if (!append) list.replaceChildren();
        data.results.forEach(function (item) { list.appendChild(row(item)); });
        expandedPage = append;
        cursor = data.next_before;
        more.hidden = !cursor;
        message.textContent = list.children.length ? '' : (filter ? 'No results match this ID.' : 'No saved label results yet.');
      } catch (err) {
        if (ticket === generation && err.status !== 401) message.replaceChildren(errBox(err));
      } finally { if (ticket === generation) more.disabled = false; }
    }
    root.append(h('h2', {text:'Label results'}), h('form', {class:'row', onsubmit:function (e) {
      e.preventDefault(); filter = query.value.trim(); load(false);
    }}, query, h('button', {class:'btn', type:'submit', text:'Search'}),
    h('button', {class:'btn', type:'button', text:'Show all', onclick:function () {query.value = filter = ''; load(false);}})),
    message,list,more);
    more.onclick = function () { load(true); };
    main.appendChild(root);
    load(false);
    var timer = setInterval(function () {
      if (!root.isConnected) {clearInterval(timer);return;}
      if (!document.hidden && !expandedPage && !more.disabled && !list.querySelector('details[open]') && !list.contains(document.activeElement)) load(false);
    }, 10000);
  }

  function labelingPage() {
    setNav('labeling');
    page('Memory & label');
    var layout = h('div', { class: 'label-layout' });
    main.appendChild(layout);
    labelResultsPanel();
    var path = '/api/configs/transcript-labels';
    var enabled = h('input', { type: 'checkbox', role: 'switch' });
    var prompt = h('textarea', { class: 'label-prompt', rows: '4', required: '' });
    var dimensions = h('textarea', { rows: '9', required: '', spellcheck: 'false' });
    var definition = h('pre', { class: 'pre' });
    var status = h('div', { role: 'status' });
    var save = h('button', { type: 'submit', class: 'btn btn-primary', text: 'Save task' });
    var task = h('fieldset', { disabled: '' },
      h('legend', { text: 'Task configuration' }),
      h('label', { class: 'enable-label' }, enabled, 'Enable labeling'),
      h('label', null, 'Labeling prompt', prompt),
      h('label', null, 'Return fields (JSON)', dimensions),
      h('details', null, h('summary', { text: 'Return JSON Schema' }), definition),
      save, status);
    var manual = h('fieldset', { disabled: '' });
    var savedTask, taskVersion;
    function show(config) {
      var data = config.body;
      ++previewRevision;
      clearTimeout(previewTimer);
      savedTask = data;
      taskVersion = config.version;
      enabled.checked = !!data.enabled;
      prompt.value = data.prompt;
      dimensions.value = pretty(data.dimensions);
      definition.textContent = pretty(data.return_schema);
      task.disabled = false;
      manual.disabled = !data.enabled;
      status.textContent = 'Version ' + config.version + ' · Labeling ' + (data.enabled ? 'on' : 'off');
    }
    var previewTimer, previewRevision = 0;
    dimensions.oninput = function () {
      var revision = ++previewRevision;
      clearTimeout(previewTimer);
      definition.parentElement.open = true;
      var fields;
      try {
        fields = JSON.parse(dimensions.value);
      } catch (err) {
        definition.textContent = 'Return fields must be valid JSON.';
        return;
      }
      definition.textContent = 'Generating schema…';
      previewTimer = setTimeout(function () {
        guard(api('POST', '/api/label-schema/preview', fields)).then(function (schema) {
          if (revision === previewRevision) definition.textContent = pretty(schema);
        }).catch(function (err) {
          if (revision === previewRevision) definition.textContent = err.message;
        });
      }, 200);
    };
    enabled.onchange = function () {
      var desired = enabled.checked;
      task.disabled = true;
      guard(api('PUT', path + '?previous_version=' + taskVersion, Object.assign({}, savedTask, { enabled: desired }))).then(function (result) {
        savedTask.enabled = desired;
        taskVersion = result.config.version;
        manual.disabled = !desired;
        status.textContent = 'Version ' + result.config.version + ' · Labeling ' + (desired ? 'on' : 'off');
      }).catch(function (err) {
        enabled.checked = savedTask.enabled;
        if (err.status !== 401) status.replaceChildren(errBox(err));
      }).finally(function () { task.disabled = false; });
    };
    function load() { return api('GET', path).then(function (result) { show(result.config); }); }
    layout.appendChild(h('form', { class: 'card', onsubmit: function (event) {
      event.preventDefault();
      var fields;
      try { fields = JSON.parse(dimensions.value); } catch (err) { status.replaceChildren(errBox(err)); return; }
      task.disabled = true;
      guard(api('PUT', path + '?previous_version=' + taskVersion, { enabled: enabled.checked, prompt: prompt.value, dimensions: fields }).then(load))
        .catch(function (err) { task.disabled = false; if (err.status !== 401) status.replaceChildren(errBox(err)); });
    } }, task));
    var lead = h('select');
    var panel = h('div');
    manual.append(h('legend', { text: 'Label a transcript' }), h('label', null, 'Lead', lead), panel);
    layout.appendChild(h('section', { class: 'card' }, manual));
    guard(load()).catch(function (err) { if (err.status !== 401) status.replaceChildren(errBox(err)); });
    guard(api('GET', '/api/leads')).then(function (data) {
      data.leads.forEach(function (l) { lead.appendChild(h('option', { value: l.id, text: l.values.name + ' · ' + l.id })); });
      function select() {
        var selected = data.leads.find(function (l) { return l.id === lead.value; });
        if (selected) panel.replaceChildren(transcriptLabelForm(selected));
        else panel.textContent = 'Create a lead in Schedule to label a transcript.';
      }
      lead.onchange = select;
      select();
    }).catch(function (err) { if (err.status !== 401) panel.replaceChildren(errBox(err)); });
  }

  // ----- configs -----
  var CONFIG_HELP = {
    'transcript-labels': 'Edit the labeling prompt and dimensions. Each dimension has a key, description, group (labels, user or vehicle) and type (string, number, boolean or enum). Give enum dimensions a list of options.',
    'quote-rules': 'Placeholder rules for a quote when no business service is configured: coverages with base premiums, discounts, a simulated delay. See internal/crm/quote.go.',
    'quote-speech': 'What the assistant says around a quotation, as preset lines instead of the model’s improvisation. "prehook": said the moment the premium is being worked out (not for a missing field); "posthook": the first words of the reply that delivers the result; "template": a Jinja2 template over the result (total, lines[].coverage / choice / premium, skipped) read out right after the posthook, before the model speaks — leave it empty to let the model read the figures; "posthook_status": the result status these are for ("quote"). A template that does not render is refused on save. Optional "confirm_revisions": true collects changes after the first quote, waits for caller confirmation, and uses contextual model-generated confirmation and waiting speech. Existing configs default to false. Read by golive at the start of every call; see internal/tools/quotespeech.go.',
    'quote-plans': 'The full motor draft adds coverage_details (kind, parent, label), strict validation and all 18 selections. It keeps the current default until you change it. Amounts, percentages and shared limits remain distinct. What a quotation needs before it runs. Named plans; "default" picks one, a call can name another in its working memory (quote_plan — a lead’s own plan, once leads exist). A plan lists its coverages, their tiers ("options": {"驾乘险": ["0","30万","50万","80万"]} — a coverage chosen without one is asked for, "0" means none) and its fields: name, "for" (the coverages that need it, ["*"] for all), "question" (what the assistant asks when it is unknown), "if_unknown" (skip the coverages or stop), "source" (rules, api or any — the business service looks a car up by its plate and does not need the price). With a must field missing the tool answers status=need and the assistant asks one question at a time; only with every field known is a quotation made. "default_selection" is the fixed default quotation (coverage → true or a tier) worked out for the lead while the phone rings and read out without a question; "default_vars" stand in for a lead’s variables on a call placed without one (the test line). See internal/crm/plans.go, prefetch.go.',
    'silence-reaction': 'Silence reminders: enabled toggles the feature. Prefer rounds: an ordered array of 1–3 rounds, each with min_wait_ms, max_wait_ms (1000–120000) and templates (1–10 literal lines, 1–120 characters). All templates must be distinct across the sequence. After each reminder finishes, the next round waits its own random interval. The sequence stops after its last round and resets when accepted caller speech resumes. Do not mix rounds with legacy top-level min_wait_ms/max_wait_ms/max_attempts/templates; those old configs remain supported. Saved settings apply to new calls. Speech, recognition, playback and quotation work postpone reminders; existing hangup rules take priority. Use enabled:false to disable.',
    'streaming-guard': 'Match exact text in streamed model replies before speech synthesis. Leave the replacement empty to remove the phrase. At the same starting point, the longest matching rule wins. Save to apply the new version to new calls.',
    'turn-taking': 'Give the caller the floor on a configured cue. Use one phrase per line. A cue can stand on its own or precede a question separated by punctuation. Leave the acknowledgment blank for silence. Saved versions apply to new calls.',
    'user-prompt': 'Add business guidance to the current caller message. The caller’s words are included automatically. Use Jinja if/elif/else, set, and simple expressions. Available inputs are caller_text, events.wechat_added, and events.product_page_opened. Saved versions apply to new calls.',
    'hangup-rules': 'When the assistant ends a call. Each rule is a case: kind "model" (the chat model judges "when" and says "farewell" before hanging up — add a case by adding a line), "keyword" (a regular expression on the caller’s words; the engine says the farewell and hangs up), "silence" (no word from the caller for "seconds"), "duration" (the call has run "seconds"). "enabled": false keeps a case without using it. "min_turns" on a model rule (or "model_min_turns" on the set) makes the engine refuse the model’s end_call until the caller has had that many turns — "refused twice" hung up on one garbled line on 10-23. Read by golive at the start of every call; a bad pattern is refused on save. See internal/tools/hangup.go.',
    'openers': 'What the assistant says at the pause, before its answer, keyed by the caller’s words — the Openers page edits this as a form. Each scenario: "keywords" (any one found in the caller’s words matches; scenarios are tried in order), "kind" (question, statement or any), "phrases" (short clauses ending in a comma, at most 4 characters plus the comma — every character is latency: the answer waits for the opener to end). Saving caches every phrase in golive at once. See internal/tools/openers.go.',
    'quote-api': 'Xiaocui mappings include choice_values, value_field (deductibleRate) and tax_field (collectVehicleTax). Use only with a backend implementing that contract. The business’s quotation service: base_url up to and including /intranet, the channel ids, the coverage codes, the phone and vehicle variables and their defaults, and how long to poll. With a base_url set, quote_total goes here. See internal/crm/quoteapi.go.'
  };
  function configs(name) {
    setNav('configs');
    page('Configs', 'Business configuration the service reads, in versions, as JSON. The assistant’s quotation tool reads quote-api (the business service) or quote-rules (the placeholder); hangup-rules says when a call ends; quote-plans says what a quotation must know first; quote-speech is what is said around one; silence-reaction controls reminders when the caller stays quiet.');
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
          configLabel(c.name), h('span', { class: 'meta' }, 'v' + c.version + ' · ' + c.chars + ' chars · ' + when(c.created_at))));
      });
      if (name) loadConfig(pane, name, 0);
      else if (cs.length) location.hash = '#/configs/' + encodeURIComponent(cs[0].name);
    }).catch(function (err) { if (err.status !== 401) pane.appendChild(errBox(err)); });
  }
  function configLabel(name) {
    if (name === 'streaming-guard') return 'Streaming guard';
    if (name === 'turn-taking') return 'Turn-taking';
    if (name === 'user-prompt') return 'User prompt';
    return name;
  }
  function userPromptEditor(value) {
    var enabled = h('input', { type: 'checkbox' });
    enabled.checked = value.enabled;
    var template = h('textarea', { class: 'tall', spellcheck: 'false' });
    template.value = value.template || '';
    var example = '{% if events.product_page_opened %}\n用户已打开产品页面。若询问调整保障，引导用户在当前页面选择对应保障并核对保费。\n{% elif events.wechat_added %}\n用户已添加车险管家，但后台还未确认打开产品页面。根据当前问题说明下一步。\n{% endif %}';
    return {
      view: h('div', null, h('label', null, enabled, ' Enable user prompt'),
        h('label', null, 'Jinja template', template),
        h('div', { class: 'row' }, h('button', { class: 'btn', type: 'button', text: 'Insert tracking example', onclick: function () {
          template.value = template.value ? template.value + '\n\n' + example : example;
          template.focus();
        } }))),
      read: function () { return { enabled: enabled.checked, template: template.value }; }
    };
  }
  function turnTakingEditor(value) {
    var enabled = h('input', { type: 'checkbox' });
    enabled.checked = value.enabled;
    var wait = h('input', { type: 'number', class: 'short', min: '500', max: '30000', step: '1', value: value.wait_ms || 5000 });
    var phrases = h('textarea', { spellcheck: 'false' });
    phrases.value = (value.phrases || []).join('\n');
    var ack = h('input', { type: 'text', maxlength: '60', value: value.acknowledgment || '' });
    return {
      view: h('div', null,
        h('label', null, enabled, ' Enable turn-taking'),
        h('label', null, 'Wait after the cue (milliseconds)', wait),
        h('label', null, 'Caller cue phrases', phrases),
        h('label', null, 'Assistant acknowledgment (optional)', ack)),
      read: function () {
        if (!wait.value || !wait.checkValidity()) throw new Error('Enter a wait of 500–30000 milliseconds.');
        return { enabled: enabled.checked, wait_ms: Number(wait.value),
          phrases: phrases.value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean),
          acknowledgment: ack.value };
      }
    };
  }
  function streamingGuardEditor(value) {
    var enabled = h('input', { type: 'checkbox' });
    enabled.checked = value.enabled;
    var list = h('div');
    var rules = [];
    function add(rule) {
      var match = h('input', { type: 'text', value: rule.match || '' });
      var replacement = h('input', { type: 'text', value: rule.replacement || '' });
      var row = { match: match, replacement: replacement };
      var card = h('div', { class: 'card' },
        h('label', null, 'Blocked phrase', match),
        h('label', null, 'Replacement (optional)', replacement),
        h('div', { class: 'row' }, h('button', { class: 'btn', type: 'button', text: 'Remove rule', onclick: function () {
          rules.splice(rules.indexOf(row), 1);
          card.remove();
        } })));
      rules.push(row);
      list.appendChild(card);
      return match;
    }
    (value.rules || []).forEach(add);
    return {
      view: h('div', null, h('label', null, enabled, ' Enable streaming guard'), list,
        h('div', { class: 'row' }, h('button', { class: 'btn', type: 'button', text: 'Add rule', onclick: function () { add({}).focus(); } }))),
      read: function () {
        return { enabled: enabled.checked, rules: rules.map(function (row) {
          return { match: row.match.value, replacement: row.replacement.value };
        }) };
      }
    };
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
    var editor = null;
    if (c && c.name === 'streaming-guard') editor = streamingGuardEditor(JSON.parse(body.value));
    if (c && c.name === 'turn-taking') editor = turnTakingEditor(JSON.parse(body.value));
    if (c && c.name === 'user-prompt') editor = userPromptEditor(JSON.parse(body.value));
    var verIn = h('input', { type: 'number', class: 'short', min: '1', value: c ? c.version : 1 });
    pane.appendChild(h('div', { class: 'card' },
      h('div', { class: 'row' }, h('strong', { text: isNew ? 'New config' : configLabel(c.name) }), c ? h('span', { class: 'chip', text: 'v' + c.version }) : null, c ? h('span', { class: 'chip mono', text: c.sha }) : null, c ? h('span', { class: 'muted', text: when(c.created_at) + ' by ' + c.by + (c.note ? ' · ' + c.note : '') }) : null),
      c && CONFIG_HELP[c.name] ? h('p', { class: 'muted', text: CONFIG_HELP[c.name] }) : null,
      c ? h('div', { class: 'row' }, h('span', { class: 'muted', text: 'Open another version:' }), verIn, h('button', { class: 'btn', type: 'button', text: 'Open', onclick: function () { loadConfig(pane, c.name, +verIn.value); } })) : null));
    pane.appendChild(h('div', { class: 'card' },
      isNew ? h('label', { text: 'Name' }) : null, isNew ? nameIn : null,
      window.GoLiveMotorCatalog.editor({h:h,api:api,errBox:errBox}, c ? c.name : '', body, note, msg),
      editor ? editor.view : [h('label', { text: 'JSON' }), body],
      h('label', { text: 'Note' }), note,
      h('div', { class: 'row' },
        editor ? null : h('button', { class: 'btn', type: 'button', text: 'Tidy', onclick: function () { try { body.value = pretty(body.value); } catch (e) { /* leave as is */ } } }),
        h('button', { class: 'btn btn-primary', type: 'button', text: isNew ? 'Save v1' : 'Save as v' + (c.version + 1), onclick: function () {
          var name = isNew ? nameIn.value.trim() : c.name;
          if (!name) { msg.replaceChildren(errBox(new Error('a name is needed'))); return; }
          var parsed;
          try { parsed = editor ? editor.read() : JSON.parse(body.value); } catch (e) {
            msg.replaceChildren(errBox(editor ? e : new Error('not JSON: ' + e.message))); return;
          }
          guard(api('PUT', '/api/configs/' + encodeURIComponent(name) + '?note=' + encodeURIComponent(note.value), parsed)).then(function (d) {
            tip('Saved ' + d.config.name + ' v' + d.config.version);
            location.hash = '#/configs/' + encodeURIComponent(name);
            configs(name);
          }).catch(function (err) { if (err.status !== 401) msg.replaceChildren(errBox(err)); });
        } })),
      msg));
  }

  // ----- openers -----
  // The `openers` config as a form: one card per scenario. Save writes the
  // config (a new version) and golive caches every phrase at once — the
  // result line says how many it added.
  function openers() {
    setNav('openers');
    page('Openers', 'What the assistant says at the pause, before its answer, chosen by what the caller just said: a question about the price opens with “价格的话，” instead of a bare “嗯，”. Scenarios are tried top to bottom; the first whose keyword is in the caller’s words wins; none matching, the profile’s own pools stand. Saving caches every phrase in golive on the spot, so the next turn that fits hears it at the pause.');
    var pane = h('div');
    main.appendChild(pane);
    pane.appendChild(h('p', { class: 'muted', text: 'Loading…' }));
    guard(api('GET', '/api/configs/openers')).then(function (d) {
      var body = d.config.body;
      if (typeof body === 'string') { try { body = JSON.parse(body); } catch (e) { body = { scenarios: [] }; } }
      showOpeners(pane, d.config, body.scenarios || []);
    }).catch(function (err) { if (err.status !== 401) { clear(pane); pane.appendChild(errBox(err)); } });
  }
  function showOpeners(pane, c, scenarios) {
    clear(pane);
    var rows = [];
    var list = h('div');
    var msg = h('div');
    function scenarioCard(sc) {
      var id = h('input', { type: 'text', placeholder: 'id (letters, e.g. price)', value: sc.id || '' });
      var name = h('input', { type: 'text', placeholder: 'name (for this page)', value: sc.name || '' });
      var kind = h('select');
      [['', 'any turn'], ['question', 'questions only'], ['statement', 'statements only']].forEach(function (o) {
        var opt = h('option', { value: o[0], text: o[1] }); if ((sc.kind || '') === o[0] || (sc.kind === 'any' && o[0] === '')) opt.selected = true; kind.appendChild(opt);
      });
      var kws = h('input', { type: 'text', placeholder: 'keywords, comma-separated: 多少钱, 价格, 保费', value: (sc.keywords || []).join('，') });
      var phs = h('input', { type: 'text', placeholder: 'openers, one per line or comma-separated: 价格的话，', value: (sc.phrases || []).join(' / ') });
      var enabled = h('input', { type: 'checkbox' }); enabled.checked = sc.enabled !== false;
      var card = h('div', { class: 'card' });
      var row = { card: card, read: function () {
        var split = function (v) { return v.split(/[\n,，;；\/]+/).map(function (x) { return x.trim(); }).filter(Boolean); };
        var out = { id: id.value.trim(), name: name.value.trim(), keywords: split(kws.value), phrases: split(phs.value) };
        if (kind.value) out.kind = kind.value;
        if (!enabled.checked) out.enabled = false;
        return out;
      } };
      card.appendChild(h('div', { class: 'row' },
        h('label', { class: 'row' }, enabled, ' enabled'),
        h('button', { class: 'btn', type: 'button', text: '↑', title: 'earlier (tried first)', onclick: function () { var i = rows.indexOf(row); if (i > 0) { rows.splice(i, 1); rows.splice(i - 1, 0, row); list.insertBefore(card, rows[i].card); } } }),
        h('button', { class: 'btn', type: 'button', text: '↓', title: 'later', onclick: function () { var i = rows.indexOf(row); if (i >= 0 && i < rows.length - 1) { rows.splice(i, 1); rows.splice(i + 1, 0, row); list.insertBefore(rows[i].card, card); } } }),
        h('button', { class: 'btn', type: 'button', text: 'Remove', onclick: function () { var i = rows.indexOf(row); if (i >= 0) rows.splice(i, 1); card.remove(); } })));
      card.appendChild(h('div', { class: 'grid2' }, h('div', null, h('label', { text: 'Id' }), id), h('div', null, h('label', { text: 'Name' }), name)));
      card.appendChild(h('label', { text: 'Applies to' })); card.appendChild(kind);
      card.appendChild(h('label', { text: 'Keywords (any one in the caller’s words matches)' })); card.appendChild(kws);
      card.appendChild(h('label', { text: 'Openers (ending in a comma, at most 4 characters + the comma — four render at the 540 ms cap, 嗯 at ~440; one is chosen, never the same twice running)' })); card.appendChild(phs);
      rows.push(row);
      return card;
    }
    scenarios.forEach(function (sc) { list.appendChild(scenarioCard(sc)); });
    var note = h('input', { type: 'text', placeholder: 'what changed (optional)' });
    pane.appendChild(h('div', { class: 'card' },
      h('div', { class: 'row' }, h('strong', { text: 'openers' }), h('span', { class: 'chip', text: 'v' + c.version }), h('span', { class: 'muted', text: when(c.created_at) + ' by ' + c.by + (c.note ? ' · ' + c.note : '') }),
        h('a', { href: '#/configs/openers', class: 'muted', text: 'edit as JSON' }))));
    pane.appendChild(list);
    pane.appendChild(h('div', { class: 'card' },
      h('div', { class: 'row' },
        h('button', { class: 'btn', type: 'button', text: 'Add scenario', onclick: function () { var card = scenarioCard({}); list.appendChild(card); card.scrollIntoView({ behavior: 'smooth', block: 'center' }); } })),
      h('label', { text: 'Note' }), note,
      h('div', { class: 'row' },
        h('button', { class: 'btn btn-primary', type: 'button', text: 'Save as v' + (c.version + 1) + ' and cache the openers', onclick: function () {
          var cfg = { scenarios: rows.map(function (r) { return r.read(); }) };
          msg.replaceChildren(h('p', { class: 'muted', text: 'Saving, and synthesizing the openers into golive’s cache…' }));
          guard(api('PUT', '/api/configs/openers?note=' + encodeURIComponent(note.value), cfg)).then(function (d) {
            var k = d.cached || {};
            var line = k.ok ? ('Saved v' + d.config.version + '. golive is caching the openers: ' + (k.queued || 0) + ' new phrase(s) being synthesized now' + (k.queued ? ' (about ' + (k.eta_s || 0) + ' s, paced for the synthesizer’s rate limit)' : '') + ', ' + (k.already || 0) + ' already cached.')
              : ('Saved v' + d.config.version + ', but golive did not cache them: ' + (k.error || 'no answer') + '. They will be cached when golive next starts or a call begins.');
            tip('Saved openers v' + d.config.version);
            msg.replaceChildren(h('p', { class: k.ok ? 'ok' : 'warn', text: line }));
            c.version = d.config.version;
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
    main.appendChild(window.GoLiveMotorCatalog.selector({h:h,api:api,errBox:errBox},body,out));
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

  function checkSkillsAccess() {
    var currentToken = token();
    var tab = document.getElementById('skills-nav');
    return api('GET', '/api/skills-tools/access').then(function (access) {
      if (currentToken === token()) tab.hidden = !access.enabled;
      return currentToken === token() && access.enabled;
    }).catch(function () {
      if (currentToken === token()) tab.hidden = true;
      return false;
    });
  }

  // ----- routing -----
  function route() {
    health();
    if (!token()) { askToken(''); return; }
    var skillsAccess = checkSkillsAccess();
    var hash = location.hash.replace(/^#\/?/, '');
    var q = {}, qi = hash.indexOf('?');
    if (qi >= 0) { hash.slice(qi + 1).split('&').forEach(function (kv) { var p = kv.split('='); q[decodeURIComponent(p[0])] = decodeURIComponent(p[1] || ''); }); hash = hash.slice(0, qi); }
    var parts = hash.split('/').map(decodeURIComponent);
    switch (parts[0]) {
      case '': case 'templates': templates(parts[1] || ''); break;
      case 'leads': case 'schedule': schedule(q.tag || ''); break;
      case 'labeling': labelingPage(); break;
      case 'configs': configs(parts[1] || ''); break;
      case 'openers': openers(); break;
      case 'skills':
        setNav('skills');
        page('Skills & tools');
        skillsAccess.then(function (enabled) {
          if (!/^#\/?skills$/.test(location.hash)) return;
          if (enabled) location.replace('/skills-tools/');
          else main.appendChild(h('p', { text: 'Skills & tools is unavailable. Check your Leads sign-in.' }));
        });
        break;
      case 'render': render(q); break;
      case 'quote': quote(); break;
      case 'calls': calls(); break;
      default: templates('');
    }
  }
  window.addEventListener('hashchange', route);
  route();
})();
