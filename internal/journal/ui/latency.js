(function () {
  'use strict';
  window.JournalLatency = function (call, h, target) {
    var e = call.latency;
    if (!e) return h('p', { class: 'standing-note' }, 'Answer-content timing is unavailable for this saved call.');
    var d = e.first_content;
    function ms(value) { return value == null ? 'unavailable' : value.toLocaleString() + ' ms'; }
    var incomplete = e.missing || e.unverified || e.duplicates;
    return h('div', { class: 'def' },
      h('h3', null, 'Caller’s last sound → first answer content'),
      e.recomputed ? h('p', { class: 'standing-note' }, 'Recomputed from this call’s archived log. Older overview figures may retain their historical grouping.') : null,
      h('p', { class: 'num' }, 'p50 ' + ms(d.p50_ms) + ' · p95 ' + ms(d.p95_ms) + ' · worst ' + ms(d.max_ms)),
      h('p', { class: 'standing-note' }, d.n + ' observed responses; ' + d.at_or_above_target + ' at or above ' + target.toLocaleString() + ' ms. ' +
        e.tool_results_joined + ' quotation results linked to their original caller turn.'),
      h('p', { class: 'standing-note' }, 'Excludes opening fillers. ' +
        (incomplete ? e.missing + ' responses missing timing; ' + e.unverified + ' unverified records or links; ' + e.duplicates + ' duplicate records. This sample cannot pass the latency gate. ' : '') +
        (d.n < 20 ? 'Fewer than 20 responses: insufficient samples for the strict gate. ' : '') +
        'These measurements stop at server output; handset latency remains unmeasured.'));
  };
}());
