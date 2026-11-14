/* Call-specific filler evidence and a separate, present-day Talker check. */
(function () {
  'use strict';
  window.JournalFillers = function (call, ui) {
    var h = ui.h, events = call.fillers || [];
    var box = h('div', { class: 'filler-panel' });
    box.appendChild(h('p', { class: 'facts' }, 'No Talker generation recorded for this call.'));
    box.appendChild(h('p', { class: 'legnote' }, 'Cached openers are prepared phrases. Queued means the server scheduled audio; use the recording to hear what played.'));
    if (!events.length) {
      box.appendChild(h('p', { class: 'empty' }, call.filler_log_version ? 'No filler events in this call’s log.' : 'Filler evidence is unavailable: this saved call has no readable source log.'));
    } else {
      var source = { phrase_cache: 'Cached phrase', configured_phrase: 'Configured phrase', unknown: 'Source not logged' };
      var kind = { opener: 'Opener', backchannel: 'Backchannel', holding_filler: 'Holding filler' };
      var outcome = { queued: 'Queued', became_backchannel: 'Became a backchannel', skipped: 'Skipped', requested: 'Requested; playback not confirmed', dropped: 'Dropped', cut_short: 'Cut short' };
      var body = h('tbody');
      events.forEach(function (f) {
        var time = f.t_s >= 0 ? ui.pos(f.t_s) : '–';
        var at = ui.player && f.t_s >= 0 ? h('button', { type: 'button', class: 'linkbtn num', title: 'Play from this filler event', onclick: function () { ui.player.playFrom(Math.max(0, f.t_s - 0.3)); } }, time) : h('span', { class: 'num' }, time);
        var details = h('div', null, f.text || 'Phrase not logged');
        if (f.scenario) details.appendChild(h('div', { class: 'legnote' }, 'Scenario: ' + f.scenario));
        var result = h('div', null, outcome[f.outcome] || f.outcome);
        if (f.reason) result.appendChild(h('div', { class: 'legnote' }, f.reason));
        if (f.audio_ms) result.appendChild(h('div', { class: 'legnote' }, f.audio_ms + ' ms audio'));
        if (f.continuation_matched) result.appendChild(h('div', { class: 'legnote' }, 'Repeated opener removed from the model’s continuation'));
        body.appendChild(h('tr', null, h('td', null, at), h('td', null, kind[f.kind] || f.kind, f.turn ? h('div', { class: 'legnote' }, f.turn) : null), h('td', null, source[f.source] || 'Source not logged'), h('td', null, details), h('td', null, result)));
      });
      box.appendChild(h('div', { class: 'filler-table-scroll' }, h('table', { class: 'filler-table' }, h('caption', null, events.length + ' filler events recorded during this call'), h('thead', null, h('tr', null, ['Time', 'Kind / turn', 'Source', 'Phrase / scenario', 'Outcome'].map(function (title) { return h('th', { scope: 'col' }, title); }))), body)));
    }
    var status = h('span', null, 'Checking current Talker service…');
    var link = h('span');
    var checking = false;
    var button = h('button', { type: 'button', class: 'btn btn-quiet', onclick: check }, 'Recheck');
    box.appendChild(h('div', { class: 'filler-service' }, h('h3', null, 'Talker service now'), h('p', null, status), h('p', { class: 'legnote' }, 'This checks current availability. It does not show whether Talker was used on this call or test a new response.'), button, ' ', link));
    function check() {
      if (checking) return;
      checking = true; button.disabled = true;
      ui.api('/api/talker-status').then(function (s) {
        var checked = s.checked_at ? ' Checked ' + new Date(s.checked_at).toLocaleTimeString() + '.' : '';
        status.textContent = (!s.enabled ? 'No service configured.' : s.up ? 'Reachable: ' + s.model + ' · status probe ' + s.probe_ms + ' ms.' : 'Service not reachable or model unavailable.') + checked;
        link.replaceChildren();
        if (s.playground_url) link.appendChild(h('a', { href: s.playground_url, target: '_blank', rel: 'noopener noreferrer' }, 'Open Talker playground'));
      }).catch(function () { status.textContent = 'Current service status could not be checked.'; }).finally(function () { checking = false; button.disabled = false; });
    }
    check();
    return box;
  };
}());
