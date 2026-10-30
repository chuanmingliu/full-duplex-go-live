(function () {
  'use strict';
  window.JournalLatency = function (call, h, target) {
    var e = call.latency;
    if (!e) return h('p', { class: 'standing-note' }, 'Answer-content timing is unavailable for this saved call.');
    var d = e.first_content;
    var result = e.tool_result;
    function ms(value) { return value == null ? 'unavailable' : value.toLocaleString() + ' ms'; }
    var incomplete = e.missing || e.unverified || e.duplicates;
    return h('div', { class: 'def' },
      h('h3', null, 'Caller’s last sound → first non-opener content'),
      e.recomputed ? h('p', { class: 'standing-note' }, 'Recomputed from this call’s archived log. Older overview figures may retain their historical grouping.') : null,
      h('p', { class: 'num' }, 'p50 ' + ms(d.p50_ms) + ' · p95 ' + ms(d.p95_ms) + ' · worst ' + ms(d.max_ms)),
      h('p', { class: 'standing-note' }, d.n + ' observed responses; ' + d.at_or_above_target + ' at or above ' + target.toLocaleString() + ' ms. ' +
        e.tool_results_joined + ' missing first-content timings filled from linked results.'),
      h('p', { class: 'standing-note' }, 'Excludes opening fillers, but may include an explanatory lead-in. For a quotation request, use the linked tool-result timing below. ' +
        (incomplete ? e.missing + ' responses missing timing; ' + e.unverified + ' unverified records or links; ' + e.duplicates + ' duplicate records. This sample cannot pass the latency gate. ' : '') +
        (d.n < 20 ? 'Fewer than 20 responses: insufficient samples for the strict gate. ' : '') +
        'These measurements stop at server output; handset latency remains unmeasured.'),
      h('h3', null, 'Caller’s last sound → linked tool result'),
      result && result.n ? h('div', null,
        h('p', { class: 'num' }, 'p50 ' + ms(result.p50_ms) + ' · p95 ' + ms(result.p95_ms) + ' · worst ' + ms(result.max_ms)),
        h('p', { class: 'standing-note' }, result.n + ' observed results; ' + result.at_or_above_target + ' at or above ' + target.toLocaleString() + ' ms. ' +
          'Measured separately from an earlier explanatory sentence; these are not extra caller turns. Content gates also check this result p95.'),
        (e.tool_result_rows || []).map(function (row) {
          return h('p', { class: 'standing-note' }, row.origin_turn + ' → ' + row.result_turn + ': ' +
            ms(row.total_ms) + ' = pause ' + ms(row.end_silence_ms) + ' + result start ' + ms(row.origin_to_result_start_ms) +
            ' + result content ' + ms(row.child_first_content_ms) + (row.interrupted ? ' (interrupted after content began)' : ''));
        })) : h('p', { class: 'standing-note' }, 'No verified linked tool-result timing in this call.'));
  };
}());
