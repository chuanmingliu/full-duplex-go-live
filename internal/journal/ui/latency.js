(function () {
  'use strict';
  window.JournalLatency = function (call, h, target) {
    var e = call.latency;
    if (!e) return h('p', { class: 'standing-note' }, 'Answer-content timing is unavailable for this saved call.');
    var d = e.conversational_first_content || e.first_content;
    var result = e.tool_result;
    var quote = e.quote_readout;
    function ms(value) { return value == null ? 'unavailable' : value.toLocaleString() + ' ms'; }
    var incomplete = e.missing || e.unverified || e.duplicates || e.quote_readouts_missing || e.quote_readouts_unverified;
    return h('div', { class: 'def' },
      h('h3', null, 'Caller’s last sound → conversational content'),
      e.recomputed ? h('p', { class: 'standing-note' }, 'Recomputed from this call’s archived log. Older overview figures may retain their historical grouping.') : null,
      h('p', { class: 'num' }, 'p50 ' + ms(d.p50_ms) + ' · p95 ' + ms(d.p95_ms) + ' · worst ' + ms(d.max_ms)),
      h('p', { class: 'standing-note' }, d.n + ' observed responses; ' + d.at_or_above_target + ' at or above ' + target.toLocaleString() + ' ms. ' +
        (e.conversational_first_content ? (e.quote_only_responses || 0) + ' quote-only responses excluded.' : 'Legacy grouping may include quote-only responses.')),
      h('p', { class: 'standing-note' }, 'Excludes opening fillers; quotation completion is reported below. ' +
        (incomplete ? e.missing + ' responses missing timing; ' + e.unverified + ' unverified records or links; ' + e.duplicates + ' duplicate records. This sample cannot pass the latency gate. ' : '') +
        (d.n < 20 ? 'Fewer than 20 responses: insufficient samples for the strict gate. ' : '') +
        'These measurements stop at server output; handset latency remains unmeasured.'),
      h('h3', null, 'Caller’s last sound → linked result first content'),
      result && result.n ? h('div', null,
        h('p', { class: 'num' }, 'p50 ' + ms(result.p50_ms) + ' · p95 ' + ms(result.p95_ms) + ' · worst ' + ms(result.max_ms)),
        h('p', { class: 'standing-note' }, result.n + ' observed results; ' + result.at_or_above_target + ' at or above ' + target.toLocaleString() + ' ms. ' +
          'This may be a cached lead-in before quoted figures. These are not extra caller turns.'),
        (e.tool_result_rows || []).map(function (row) {
          return h('p', { class: 'standing-note' }, row.origin_turn + ' → ' + row.result_turn + ': ' +
            ms(row.total_ms) + ' = pause ' + ms(row.end_silence_ms) + ' + result start ' + ms(row.origin_to_result_start_ms) +
            ' + result content ' + ms(row.child_first_content_ms) + (row.interrupted ? ' (interrupted after content began)' : '') + (row.origin_evidence === 'caller_input_v1' ? ' · measured caller input; calling-turn playback not implied' : ''));
        })) : h('p', { class: 'standing-note' }, 'No verified linked result-content timing in this call.'),
      h('h3', null, 'Caller’s last sound → quotation readout'),
      !quote ? h('p', { class: 'standing-note' }, 'This saved evidence does not identify quotation readout. It cannot establish the quoted-figure latency target.') : null,
      quote && quote.n ? h('p', { class: 'num' }, 'p50 ' + ms(quote.p50_ms) + ' · p95 ' + ms(quote.p95_ms) + ' · worst ' + ms(quote.max_ms)) :
        h('p', { class: 'standing-note' }, 'No verified quotation-readout timing in this call.'),
      h('p', { class: 'standing-note' }, (quote ? quote.n : 0) + ' observed readouts; ' +
        (e.quote_readouts_missing || 0) + ' not output; ' + (e.quote_readouts_unverified || 0) + ' unverified. ' +
        'Measured when the deterministic quotation audio starts leaving the server, not when synthesis is ready. ' +
        'The conversational SLA reports quotation duration separately; missing or unverified readouts still prevent qualification.'),
      (e.quote_readout_rows || []).map(function (row) {
        return h('p', { class: 'standing-note' }, row.origin_turn + ' → ' + row.result_turn + ': ' +
          (row.status === 'observed' ? ms(row.total_ms) + ' = pause ' + ms(row.end_silence_ms) +
            ' + result start ' + ms(row.origin_to_result_start_ms) + ' + readout output ' + ms(row.child_first_readout_ms) : row.status));
      }));
  };
}());
