/* Inspect the saved request bytes. All recorded content is rendered as text. */
window.JournalRequests = function (h, sessionID, requestID) {
  'use strict';
  var controller = new AbortController();
  var root = h('div', {class: 'request-browser'});
  var sessions = h('div');
  var requests = h('div');
  var detail = h('div', {class: 'request-detail'});
  var search = h('input', {type: 'search', placeholder: 'Session ID', 'aria-label': 'Find a session ID'});
  var sessionOffset = 0;
  var requestOffset = 0;
  var searchVersion = 0;
  var listVersion = 0;
  var query = '';

  function path(session, request) {
    return '#/requests/' + encodeURIComponent(session) + (request ? '/' + encodeURIComponent(request) : '');
  }
  function api(url) {
    return fetch(url, {cache: 'no-store', signal: controller.signal}).then(function (response) {
      return response.json().then(function (data) {
        if (!response.ok) throw new Error(data.error || 'Unable to read recordings.');
        return data;
      });
    });
  }
  function errorAt(node, error) {
    if (error.name !== 'AbortError') node.replaceChildren(h('p', {class: 'request-error', role: 'alert'}, error.message));
  }
  function date(value) { return value && !value.startsWith('0001-') ? new Date(value).toLocaleString() : 'Pending'; }
  function json(value) { return JSON.stringify(value, null, 2); }
  function block(title, text) {
    return h('section', {class: 'request-block'}, h('h3', null, title), h('pre', null, text));
  }
  function pager(node, offset, data, load) {
    node.appendChild(h('div', {class: 'request-pager'},
      h('button', {class: 'btn', disabled: offset === 0, onclick: function () { load(Math.max(0, offset - 50)); }}, 'Previous'),
      h('span', null, data.total ? (offset + 1) + '–' + Math.min(offset + 50, data.total) + ' of ' + data.total : '0 results'),
      h('button', {class: 'btn', disabled: !data.more, onclick: function () { load(offset + 50); }}, 'Next')));
  }
  function loadSessions(offset) {
    sessionOffset = offset;
    var version = ++searchVersion;
    sessions.replaceChildren(h('p', {role: 'status'}, 'Loading sessions…'));
    return api('/api/requests/sessions?offset=' + offset + '&q=' + encodeURIComponent(query)).then(function (data) {
      if (version !== searchVersion) return;
      sessions.replaceChildren();
      data.sessions.forEach(function (session) {
        sessions.appendChild(h('a', {class: 'request-session', href: path(session.id), 'aria-current': session.id === sessionID ? 'page' : null},
          h('code', null, session.id), h('small', null, 'Updated ' + date(session.updated_at))));
      });
      if (!data.sessions.length) sessions.appendChild(h('p', {class: 'empty'}, 'No recorded sessions match. Recording begins after it is enabled.'));
      pager(sessions, offset, data, loadSessions);
    }).catch(function (error) { if (version === searchVersion) errorAt(sessions, error); });
  }
  function loadRequests(offset) {
    requestOffset = offset;
    var version = ++listVersion;
    requests.replaceChildren(h('p', {role: 'status'}, 'Loading requests…'));
    return api('/api/requests/sessions/' + encodeURIComponent(sessionID) + '?offset=' + offset).then(function (data) {
      if (version !== listVersion) return;
      var table = h('table', {class: 'request-table'}, h('caption', null, 'Recorded requests in chronological order'));
      table.appendChild(h('thead', null, h('tr', null, ['Time', 'Turn / purpose', 'Outcome', 'Lane', 'Request'].map(function (label) { return h('th', {scope: 'col'}, label); }))));
      var body = h('tbody');
      data.requests.forEach(function (request) {
        body.appendChild(h('tr', {class: request.id === requestID ? 'request-selected' : ''},
          h('td', null, date(request.created_at)), h('td', null, request.turn_id || '—', h('small', null, request.purpose || 'Unknown')),
          h('td', null, request.error || request.outcome, h('small', null, request.http_status ? 'HTTP ' + request.http_status : '')),
          h('td', null, request.lane), h('td', null, h('a', {href: path(sessionID, request.id), 'aria-current': request.id === requestID ? 'true' : null}, request.id))));
      });
      table.appendChild(body);
      requests.replaceChildren(h('div', {class: 'request-table-wrap'}, table));
      if (!data.requests.length) requests.appendChild(h('p', {class: 'empty'}, 'This session has no published request records yet.'));
      pager(requests, offset, data, loadRequests);
    }).catch(function (error) { if (version === listVersion) errorAt(requests, error); });
  }
  function content(message) {
    return typeof message.content === 'string' ? message.content : json(message.content === undefined ? message : message.content);
  }
  function skillsView(messages) {
    var node = h('div');
    var found = false;
    var embedded = false;
    var skillCalls = new Set();
    messages.forEach(function (message) {
      if (message.role !== 'assistant') return;
      (message.tool_calls || []).forEach(function (call) {
        if (call.function && call.function.name === 'load_business_skill') skillCalls.add(call.id);
      });
    });
    messages.forEach(function (message) {
      if (!((message.role === 'system' && message.name === 'load_business_skill') ||
            (message.role === 'tool' && skillCalls.has(message.tool_call_id)))) return;
      var data;
      try { data = JSON.parse(message.content); } catch (_) { return; }
      if (!data || !Array.isArray(data.guidance)) return;
      embedded = embedded || data.rendering_version === 2;
      data.guidance.forEach(function (guide) {
        found = true;
        var card = h('section', {class: 'request-block'},
          h('h3', null, (guide.uri || '').indexOf('skill://memory-') === 0 ? 'Dreaming memory' : 'Injected skill'),
          h('code', null, guide.uri || 'Unspecified skill'),
          h('p', null, 'Status ' + (data.status || 'unknown') + ' · renderer ' + (data.rendering_version || 1) + ' · memory version ' + data.memory_version),
          h('p', null, 'Digest ', h('code', null, guide.digest || 'Unavailable')));
        if (guide.required_state) card.appendChild(block('Required state', json(guide.required_state)));
        card.appendChild(guide.text ? h('pre', null, guide.text) : h('p', null, 'The skill text is embedded in the rendered system prompt below.'));
        node.appendChild(card);
      });
    });
    messages.forEach(function (message) {
      if (!embedded || message.role !== 'system' || typeof message.content !== 'string') return;
      var start = message.content.indexOf('<additional_skill_instructions>');
      var end = message.content.lastIndexOf('</additional_skill_instructions>');
      if (start >= 0 && end > start) node.appendChild(block('Embedded skill instructions', message.content.slice(start + 31, end).trim()));
    });
    if (!found) node.appendChild(h('p', {class: 'empty'}, 'No injected skill provenance is present in this request.'));
    return node;
  }
  function renderDetail(data) {
    var body;
    try { body = JSON.parse(data.raw_request); } catch (_) { body = {}; }
    var messages = Array.isArray(body.messages) ? body.messages : [];
    var tabs = h('div', {class: 'request-tabs', role: 'tablist', 'aria-label': 'Request views'});
    var pane = h('div', {id: 'request-pane', role: 'tabpanel', tabindex: '0'});
    var views = {
      'Rendered prompt': function () {
        var node = h('div');
        messages.filter(function (m) { return m.role === 'system' || m.role === 'developer'; }).forEach(function (m, index) { node.appendChild(block((index + 1) + '. ' + m.role + (m.name ? ' · ' + m.name : ''), content(m))); });
        if (!node.childNodes.length) node.appendChild(h('p', {class: 'empty'}, 'No system or developer messages are present.'));
        return node;
      },
      'Conversation': function () {
        return h('div', null, messages.map(function (m, index) {
          var node = block((index + 1) + '. ' + m.role + (m.name ? ' · ' + m.name : ''), content(m));
          if (m.tool_calls) node.appendChild(block('Tool calls', json(m.tool_calls)));
          if (m.tool_call_id) node.appendChild(h('p', null, 'Tool call ID ', h('code', null, m.tool_call_id)));
          return node;
        }));
      },
      'Memory skills': function () { return skillsView(messages); },
      'Metadata': function () { return block('Recorded metadata', json(data.metadata)); },
      'Raw request': function () { return block('Exact stored HTTP request body', data.raw_request); }
    };
    function select(name) {
      Array.from(tabs.children).forEach(function (button) { button.setAttribute('aria-selected', String(button.textContent === name)); button.tabIndex = button.textContent === name ? 0 : -1; });
      pane.setAttribute('aria-labelledby', 'request-tab-' + Object.keys(views).indexOf(name));
      pane.replaceChildren(views[name]());
    }
    Object.keys(views).forEach(function (name, index) {
      tabs.appendChild(h('button', {id: 'request-tab-' + index, role: 'tab', 'aria-controls': 'request-pane', onclick: function () { select(name); }, onkeydown: function (event) {
        var names = Object.keys(views), next;
        if (event.key === 'ArrowRight') next = (index + 1) % names.length;
        if (event.key === 'ArrowLeft') next = (index + names.length - 1) % names.length;
        if (event.key === 'Home') next = 0;
        if (event.key === 'End') next = names.length - 1;
        if (next !== undefined) { event.preventDefault(); select(names[next]); tabs.children[next].focus(); }
      }}, name));
    });
    var download = h('button', {class: 'btn', onclick: function () {
      var url = URL.createObjectURL(new Blob([data.raw_request], {type: 'application/json'}));
      var link = h('a', {href: url, download: sessionID + '-' + data.metadata.id + '.json'});
      link.click(); setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
    }}, 'Download raw JSON');
    detail.replaceChildren(h('div', {class: 'request-detail-head'}, h('h2', null, 'Request details'), download),
      h('code', null, data.metadata.id), h('p', {class: data.integrity_ok ? 'request-integrity' : 'request-error'}, data.integrity_ok ? 'SHA-256 and byte count verified' : 'Integrity mismatch — stored bytes differ from the recorded metadata'), tabs, pane);
    select('Rendered prompt');
  }
  var form = h('form', {class: 'request-search', onsubmit: function (event) { event.preventDefault(); query = search.value.trim(); loadSessions(0); }},
    h('label', null, 'Find a session', search), h('button', {class: 'btn', type: 'submit'}, 'Search'));
  var direct = h('form', {class: 'request-search', onsubmit: function (event) {
    event.preventDefault(); var id = direct.querySelector('input').value.trim(); if (id) location.hash = path(id);
  }}, h('label', null, 'Open session ID', h('input', {required: true, value: sessionID || '', pattern: '[A-Za-z0-9_-]{1,128}'})), h('button', {class: 'btn', type: 'submit'}, 'Open'));
  root.appendChild(h('header', {class: 'request-heading'}, h('h1', null, 'Requests'), h('p', null, 'Inspect the rendered prompts, memory skills and raw API requests saved for each session.')));
  root.appendChild(h('div', {class: 'request-layout'}, h('aside', null, form, sessions),
    h('section', {class: 'request-main'}, direct, sessionID ? h('h2', {class: 'request-session-title'}, sessionID) : null, requests, detail)));
  if (!sessionID) requests.appendChild(h('p', {class: 'empty'}, 'Select a session to view its requests.'));
  else if (!requestID) detail.appendChild(h('p', {class: 'empty'}, 'Select a request to inspect its prompt and metadata.'));
  var work = [loadSessions(sessionOffset)];
  if (sessionID) work.push(loadRequests(requestOffset));
  if (sessionID && requestID) {
    detail.appendChild(h('p', {role: 'status'}, 'Loading request…'));
    work.push(api('/api/requests/sessions/' + encodeURIComponent(sessionID) + '/' + encodeURIComponent(requestID)).then(renderDetail).catch(function (error) { errorAt(detail, error); }));
  }
  return {element: root, ready: Promise.all(work), stop: function () { controller.abort(); }};
};
