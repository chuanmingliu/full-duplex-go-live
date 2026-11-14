/* Skill preparation timing from the selected session's archive. */
window.JournalSkills = function (call, h) {
  var rows = call.skill_traces || [];
  if (!rows.length) return h('p', {class: 'facts'}, 'No skill preparation evidence was recorded for this call.');
  function cost(us) { return (us / 1000).toFixed(3) + ' ms'; }
  var box = h('div');
  rows.forEach(function (row) {
    var elapsed = row.us == null ? row.ms + ' ms' : cost(row.us);
    var trace = row.trace;
    var title = [row.t_s.toFixed(2) + ' s', row.turn, row.status, elapsed].filter(Boolean).join(' · ');
    var item = h('details', null, h('summary', null, title));
    box.appendChild(item);
    if (!trace) {
      item.appendChild(h('p', {class:'legnote'}, 'Renderer v' + row.rendering_version + '. Only the aggregate preparation was recorded.'));
      return;
    }
    item.appendChild(h('p', {class:'facts'}, [
      'Renderer v' + row.rendering_version, trace.revision && 'catalog r' + trace.revision,
      'memory v' + row.memory_start + ' → v' + row.memory_end, row.response
    ].filter(Boolean).join(' · ')));
    var body = h('tbody');
    trace.steps.forEach(function (step) {
      body.appendChild(h('tr', null, h('td', null, step.stage), h('td', null, step.uri || '', step.digest ? h('div', {class:'legnote'}, step.digest) : null), h('td', null, cost(step.us)), h('td', null, step.outcome)));
    });
    item.appendChild(h('div', {class:'filler-table-scroll'}, h('table', {class:'filler-table'}, h('thead', null, h('tr', null, ['Stage','Resource','Time','Outcome'].map(function (head) {return h('th', {scope:'col'}, head);}))), body)));
  });
  return box;
};
