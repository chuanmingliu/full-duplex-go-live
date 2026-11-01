/* Separate journals retain their own calls, recordings and qualification notes. */
window.JournalExperiments = function (view, helpers) {
  'use strict';
  var h = helpers.h, disposed = false;
  var status = h('p', { role: 'status' }, 'Loading experiment journals…');
  var choices = h('div', { class: 'experiment-choices', role: 'group', 'aria-label': 'Choose an experiment journal' });
  var description = h('p', { class: 'lede', 'aria-live': 'polite' });
  var frame = h('iframe', { class: 'experiment-frame', title: 'Selected experiment Call Journal', hidden: true });
  var open = h('a', { target: '_blank', rel: 'noopener', hidden: true }, 'Open this journal in a full page');
  view.appendChild(h('section', { class: 'experiment-panel' },
    h('h1', null, 'SIP/RTP tests'),
    h('p', { class: 'lede' }, 'Original GoLive with the SIP/RTP adapter. Replay audio, read the conversation and inspect each call’s review.'),
    h('p', { class: 'standing-note' }, 'Real calls and simulations are separate. Server and simulator timings do not measure sound reaching the handset. Failed or incomplete tests remain in the journals; the 1,200 ms handset target is not yet established.'),
    choices, status, description, open, frame));
  helpers.api('/api/experiments').then(function (sources) {
    if (disposed) return;
    status.textContent = sources.length ? '' : 'No experiment journals configured yet.';
    function select(source, button) {
      Array.from(choices.children).forEach(function (b) { b.setAttribute('aria-pressed', String(b === button)); });
      description.textContent = (source.kind === 'simulated' ? 'Simulation · ' : 'Real phone calls · ') + source.description;
      var path = '/experiments/' + encodeURIComponent(source.id) + '/';
      frame.title = source.title + ' — recordings, transcripts and reviews';
      frame.src = path;
      frame.hidden = false;
      open.href = path;
      open.hidden = false;
    }
    sources.forEach(function (source, i) {
      var button = h('button', { type: 'button', class: 'btn', 'aria-pressed': 'false', onclick: function () { select(source, button); } }, source.title);
      choices.appendChild(button);
      if (i === 0) select(source, button);
    });
  }).catch(function (err) { if (!disposed) status.textContent = 'Could not load experiment journals: ' + err.message; });
  return function () { disposed = true; frame.src = 'about:blank'; };
};
