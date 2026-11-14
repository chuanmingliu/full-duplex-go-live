/* Editable cron review tasks. No shell commands are accepted or executed. */
window.JournalDreamingSchedules = function (h, tasks) {
  'use strict';
  var rows = [];
  var list = h('div', {class: 'dream-cron-editor'});
  function add(task) {
    var name = h('input', {type: 'text', value: task.name, required: true, maxlength: '100', 'aria-label': 'Task name'});
    var expression = h('input', {type: 'text', value: task.expression, required: true, maxlength: '100', 'aria-label': 'Cron expression'});
    var timezone = h('input', {type: 'text', value: task.timezone, required: true, maxlength: '100', 'aria-label': 'Timezone'});
    var enabled = h('input', {type: 'checkbox', 'aria-label': 'Enable cron task'});
    enabled.checked = task.enabled;
    var row = {id: task.id, name: name, expression: expression, timezone: timezone, enabled: enabled};
    var element = h('fieldset', {class: 'dream-cron-row'},
      h('legend', null, 'Review task'), h('label', null, enabled, ' Enabled'),
      h('label', null, 'Task name', name), h('label', null, 'Cron expression', expression),
      h('label', null, 'Timezone', timezone),
      h('button', {type: 'button', class: 'btn', onclick: function () {
        rows = rows.filter(function (item) { return item !== row; });
        element.remove();
      }}, 'Remove task'));
    rows.push(row);
    list.appendChild(element);
  }
  tasks.forEach(add);
  var element = h('section', null, h('h3', null, 'Cron task book'),
    h('p', null, 'Minute · hour · day of month · month · day of week. For example, 0 2 * * * runs daily at 02:00 in the selected timezone.'),
    h('p', null, 'Cron tasks review available, unreviewed calls. Turn off after-hangup queueing for cron-only operation. Due tasks wait through active calls and service restarts.'), list,
    h('button', {type: 'button', class: 'btn', onclick: function () {
      add({id: 'cron-' + Date.now().toString(36) + Math.random().toString(36).slice(2, 7), name: 'Nightly review', enabled: true, expression: '0 2 * * *', timezone: 'Asia/Shanghai'});
    }}, 'Add cron task'));
  return {element: element, value: function () {
    return rows.map(function (row) { return {id: row.id, name: row.name.value, enabled: row.enabled.checked, expression: row.expression.value, timezone: row.timezone.value}; });
  }};
};

window.JournalDreamingTaskBook = function (h, tasks, runs, enabled) {
  'use strict';
  function time(value) { return value && value.slice(0, 4) !== '0001' ? new Date(value).toLocaleString() : '—'; }
  var root = h('section', {class: 'dream-card'}, h('h3', null, 'Cron task book'));
  if (!tasks.length) root.appendChild(h('p', null, 'Add a scheduled review in Dreaming setup.'));
  tasks.forEach(function (task) {
    var pending = task.pending_since && task.pending_since.slice(0, 4) !== '0001';
    var status = !enabled || !task.enabled ? 'Paused' : pending ? 'Due · waiting for idle dispatch' : 'Scheduled';
    root.appendChild(h('div', {class: 'dream-cron-status'}, h('strong', null, task.name),
      h('span', null, status), h('code', null, task.expression + ' · ' + task.timezone),
      h('span', null, 'Next run ' + time(task.next_run)),
      pending ? h('span', null, 'Pending since ' + time(task.pending_since)) : null));
  });
  if (runs.length) {
    var history = h('details', null, h('summary', null, 'Cron dispatch history'));
    runs.forEach(function (run) {
      history.appendChild(h('div', {class: 'dream-cron-status'},
        h('strong', null, run.name), h('span', null, 'Due ' + time(run.scheduled_at) + ' · dispatched ' + time(run.dispatched_at)),
        h('span', null, run.queued + ' reviews queued · ' + run.failures + ' capture failures'),
        h('code', null, run.job_ids.join(', ') || 'No new review-ready calls'),
        run.queued > run.job_ids.length ? h('span', null, 'Showing the first ' + run.job_ids.length + ' job IDs') : null));
    });
    root.appendChild(history);
  }
  return root;
};
