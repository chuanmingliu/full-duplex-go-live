/* Live state is served by the Journal backend. Credentials stay server-side. */
window.JournalDreamingLive = function (h) {
  'use strict';
  var root = h('section', {class: 'dream-live'});
  var status = h('div', {class: 'dream-card', 'aria-live': 'polite'}, 'Loading live dreaming status…');
  var settingsView = h('div');
  var jobsView = h('div');
  var callsView = h('div');
  var message = h('p', {role: 'status'});
  var settings = null;
  var stopped = false;
  var timer = null;
  var selected = null;
  var detailRequest = 0;
  var selectedDetails = h('div');
  var formBuilt = false;
  root.append(status, settingsView, message, callsView, jobsView, selectedDetails);

  function time(value) {
    return value && value.slice(0, 4) !== '0001' ? new Date(value).toLocaleString() : 'Pending';
  }
  function request(path, method, body) {
    return fetch('/api/dreaming/' + path, {
      method: method || 'GET', cache: 'no-store',
      headers: {'Content-Type': 'application/json'},
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (response) {
      return response.json().then(function (data) {
        if (!response.ok) throw new Error(data.error || 'Dreaming request failed');
        return data;
      });
    });
  }
  function buildSettings(value) {
    if (formBuilt) return;
    settings = value;
    formBuilt = true;
    var enabled = h('input', {type: 'checkbox', id: 'dream-enabled'});
    enabled.checked = value.enabled;
    var delay = h('input', {type: 'number', id: 'dream-idle', min: '0', max: '3600', required: true, value: value.idle_seconds});
    var simulations = h('input', {type: 'checkbox', id: 'dream-simulations'});
    simulations.checked = value.include_simulation;
    var save = h('button', {type: 'submit', class: 'btn btn-main'}, 'Save dreaming settings');
    var form = h('form', {class: 'dream-settings'},
      h('label', {for: 'dream-enabled'}, enabled, ' Enable automatic capture and background review'),
      h('label', {for: 'dream-idle'}, 'Idle delay in seconds', delay),
      h('label', {for: 'dream-simulations'}, simulations, ' Include simulator calls'),
      h('p', null, 'New completed calls are reviewed using the configured model. Text evidence and owner feedback go to that model. Audio stays in the local archive. Lessons require review before publication.'), save);
    form.addEventListener('submit', function (event) {
      event.preventDefault();
      if (!form.reportValidity()) return;
      save.disabled = true;
      request('settings', 'PUT', {
        revision: settings.revision, enabled: enabled.checked,
        idle_seconds: Number(delay.value), include_simulation: simulations.checked,
        since: settings.since
      }).then(function (saved) {
        settings = saved;
        message.textContent = 'Dreaming settings saved.';
        return refresh();
      }).catch(function (error) { message.textContent = error.message; })
        .finally(function () { save.disabled = false; });
    });
    settingsView.replaceChildren(h('details', {class: 'dream-card'}, h('summary', null, 'Dreaming setup'), form));
  }
  function renderDetails(job, publications) {
    if (!job) { selectedDetails.replaceChildren(); return; }
    var expanded = Array.from(selectedDetails.querySelectorAll('details[open]')).map(function (node) { return node.dataset.lesson; });
    var card = h('article', {class: 'dream-card'}, h('h3', null, job.id), h('p', null, 'Status · ' + job.status));
    var links = h('div', {class: 'dream-call-links'});
    (job.call_ids || []).forEach(function (id) { links.appendChild(h('a', {href: '#/call/' + encodeURIComponent(id)}, id)); });
    card.appendChild(links);
    if (job.error) card.appendChild(h('p', {class: 'dream-error'}, job.error));
    if (job.status === 'failed') {
      card.appendChild(h('button', {class: 'btn', type: 'button', onclick: function (event) {
        event.currentTarget.disabled = true;
        request('jobs/' + encodeURIComponent(job.id) + '/retry', 'POST', {}).then(refresh)
          .catch(function (error) { message.textContent = error.message; });
      }}, 'Retry review'));
    }
    var events = h('ol', {class: 'dream-events'});
    (job.events || []).forEach(function (event) {
      events.appendChild(h('li', null, h('time', {datetime: event.at}, time(event.at)),
        h('strong', null, event.stage), h('span', null, event.detail)));
    });
    card.appendChild(h('h4', null, 'Recorded process'));
    card.appendChild(events);
    if (!(job.events || []).length) card.appendChild(h('p', null, 'This earlier job predates process-event recording.'));
    (job.lessons || []).forEach(function (lesson) {
      card.appendChild(h('details', {class: 'dream-block', 'data-lesson': lesson.id, open: expanded.indexOf(lesson.id) >= 0},
        h('summary', null, lesson.title + ' · ' + ((publications || {})[lesson.id] || 'candidate')),
        h('p', null, lesson.observation), h('p', null, lesson.lesson),
        h('pre', null, JSON.stringify({id: lesson.id, evidence: lesson.evidence, match: lesson.match, exclude: lesson.exclude, cases: lesson.cases}, null, 2))));
    });
    selectedDetails.replaceChildren(card);
  }
  function render(data) {
    buildSettings(data.settings);
    var activity = data.activity || {};
    var label = activity.status || 'unknown';
    var waiting = data.settings.enabled ? 'Waiting for completed calls' : 'Automatic capture and reviews paused';
    if (data.settings.enabled && label === 'busy') waiting = 'Waiting for active calls to finish';
    if (data.settings.enabled && label === 'unknown') waiting = 'Waiting for voice-engine activity';
    if (label === 'idle' && data.settings.enabled) {
      var elapsed = (Date.parse(activity.checked_at) - Date.parse(activity.idle_since)) / 1000;
      var remaining = Math.max(0, Math.ceil(data.settings.idle_seconds - elapsed));
      waiting = remaining > 0 ? 'Idle · reviews eligible in ' + remaining + ' seconds' : 'Idle · ready for queued reviews';
    }
    status.replaceChildren(h('h2', null, 'Live dreaming'), h('p', null, waiting),
      h('p', null, 'Active sessions · ' + (activity.sessions || 0) + '   |   Worker · ' + (data.worker_enabled ? 'configured' : 'model setup required')),
      h('p', null, 'Activity checked ' + time(activity.checked_at) + '   |   Idle since ' + time(activity.idle_since)),
      h('p', null, 'Call discovery checked ' + time(data.collector.checked_at) + '   |   Capture starts ' + time(data.settings.since)));
    [activity.error, data.collector.error].filter(Boolean).forEach(function (error) { status.appendChild(h('p', {class: 'dream-error'}, error)); });
    var pending = (data.calls || []).filter(function (call) { return call.status !== 'queued'; });
    callsView.replaceChildren();
    if (pending.length) {
      var list = h('ul');
      pending.forEach(function (call) { list.appendChild(h('li', null, call.id + ' · ' + call.status + (call.error ? ' · ' + call.error : ''))); });
      callsView.appendChild(h('section', {class: 'dream-card'}, h('h3', null, 'Call capture'), list));
    }
    var jobs = data.jobs || [];
    jobsView.replaceChildren(h('h3', null, 'Recent reviews'));
    if (!jobs.length) jobsView.appendChild(h('p', null, 'The first new completed call will appear here automatically.'));
    jobs.forEach(function (job) {
      var state = job.status;
      if (state === 'pending' && Date.parse(job.not_before) > Date.now()) state = 'scheduled';
      if (state === 'pending') state = !data.settings.enabled ? 'paused' : label === 'idle' ? 'queued / waiting for idle delay' : 'waiting for agent idle';
      jobsView.appendChild(h('button', {type: 'button', class: 'dream-job', 'aria-pressed': selected === job.id,
        onclick: function () { selected = job.id; render(data); }
      }, h('strong', null, (job.call_ids || []).join(', ')), h('span', null, state + ' · ' + job.lesson_count + ' lessons'), h('small', null, job.id + ' · eligible ' + time(job.not_before))));
    });
    if (selected) {
      var requested = selected;
      var sequence = ++detailRequest;
      request('jobs/' + encodeURIComponent(requested)).then(function (detail) {
        if (!stopped && selected === requested && sequence === detailRequest) renderDetails(detail.job, detail.publications);
      }).catch(function (error) { if (!stopped) message.textContent = error.message; });
    }
  }
  function refresh() {
    return request('live').then(function (data) { if (!stopped) render(data); }).catch(function (error) {
      if (!stopped) status.replaceChildren(h('h2', null, 'Live dreaming unavailable'), h('p', {class: 'dream-error'}, error.message));
    });
  }
  function poll() {
    refresh().finally(function () { if (!stopped) timer = setTimeout(poll, 3000); });
  }
  poll();
  return {element: root, stop: function () { stopped = true; clearTimeout(timer); }};
};
