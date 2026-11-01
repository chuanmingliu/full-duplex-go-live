/* Separate journals retain their own calls, recordings and qualification notes. */
window.JournalExperiments = function (view, helpers) {
  'use strict';
  var h = helpers.h, disposed = false, generation = 0;
  var status = h('p', { role: 'status' }, 'Loading experiment journals…');
  var choices = h('div', { class: 'experiment-choices', role: 'group', 'aria-label': 'Choose an experiment journal' });
  var description = h('p', { class: 'lede', 'aria-live': 'polite' });
  var calls = h('div', { class: 'experiment-calls' });
  var open = h('a', { hidden: true }, 'Open the full journal');
  function stopAudio() { calls.querySelectorAll('audio').forEach(function (a) { a.pause(); a.removeAttribute('src'); a.load(); }); }
  view.appendChild(h('section', { class: 'experiment-panel' },
    h('h1', null, 'SIP/RTP tests'),
    h('p', { class: 'lede' }, 'Original GoLive with the SIP/RTP adapter. Replay audio, read the conversation and inspect each call’s review.'),
    h('p', { class: 'standing-note' }, 'Real calls and simulations are separate. Server and simulator timings do not measure sound reaching the handset. Failed or incomplete tests remain in the journals; the 1,200 ms handset target is not yet established.'),
    choices, description, open, status, calls));
  helpers.api('/api/experiments').then(function (sources) {
    if (disposed) return;
    status.textContent = sources.length ? '' : 'No experiment journals configured yet.';
    function select(source, button) {
      var token = ++generation;
      stopAudio(); calls.replaceChildren();
      Array.from(choices.children).forEach(function (b) { b.setAttribute('aria-pressed', String(b === button)); });
      description.textContent = (source.kind === 'simulated' ? 'Simulation · ' : 'Real phone calls · ') + source.description;
      var path = '/experiments/' + encodeURIComponent(source.id) + '/';
      open.href = path; open.hidden = false;
      status.textContent = 'Loading calls…';
      helpers.api(path + 'api/overview').then(function (overview) {
        if (disposed || token !== generation) return;
        var rows = overview.calls || [];
        status.textContent = rows.length + ' calls · newest first';
        rows.forEach(function (call) {
          var url = path + 'api/calls/' + encodeURIComponent(call.id);
          var when = new Date(call.started_at).toLocaleString();
          var summary = call.summary || {};
          var audio = call.has_audio ? h('audio', { controls: true, preload: 'none', src: url + '/audio.wav', 'aria-label': 'Recording of ' + when }) : null;
          if (audio) audio.addEventListener('play', function () { calls.querySelectorAll('audio').forEach(function (other) { if (other !== audio) other.pause(); }); });
          calls.appendChild(h('article', { class: 'experiment-call' },
            h('h2', null, h('a', { href: path + '#/call/' + encodeURIComponent(call.id) }, when)),
            h('p', { class: 'experiment-meta' }, (source.kind === 'simulated' ? 'Simulation' : 'Real call') + ' · ' + (call.build || 'Build unknown') + ' · ' + (summary.turns || 0) + ' measured turns'),
            h('p', { class: 'experiment-id' }, call.id),
            h('p', null, 'Server first audio (includes fillers): median ' + (summary.turns ? summary.p50 + ' ms' : 'unavailable') + '. Full call details separate content and quotation latency.'),
            audio || h('p', null, 'No audio recording available.'),
            h('div', { class: 'experiment-links' },
              h('a', { href: path + '#/call/' + encodeURIComponent(call.id) }, 'Conversation, charts & review'),
              h('a', { href: url + '/transcript.txt?download=1' }, 'Download transcript'),
              call.has_audio ? h('a', { href: url + '/audio.wav?download=1' }, 'Download audio') : null),
            h('p', { class: 'standing-note' }, call.has_retro ? 'Post-call review available in call details.' : 'No post-call review saved.')));
        });
      }).catch(function () {
        if (disposed || token !== generation) return;
        status.textContent = 'This journal is unavailable. Choose another journal or retry.';
        calls.appendChild(h('button', { type: 'button', class: 'btn', onclick: function () { select(source, button); } }, 'Retry'));
      });
    }
    sources.forEach(function (source, i) {
      var button = h('button', { type: 'button', class: 'btn', 'aria-pressed': 'false', onclick: function () { select(source, button); } }, source.title);
      choices.appendChild(button);
      if (i === 0) select(source, button);
    });
  }).catch(function (err) { if (!disposed) status.textContent = 'Could not load experiment journals: ' + err.message; });
  return function () { disposed = true; stopAudio(); };
};
