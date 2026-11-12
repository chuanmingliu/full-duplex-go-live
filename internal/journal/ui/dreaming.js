/* Reviewed lifecycle records. API strings enter the DOM only as text nodes. */
window.JournalDreaming = function (data, h) {
  'use strict';
  var root = h('div', {class: 'dreaming'});
  var runs = data.runs || [];
  var selectedRun = 0;
  var selectedStage = 0;
  var showAll = false;
  var stageView = h('div', {class: 'dream-stage-view', id: 'dream-stage-view'});
  var rail = h('nav', {class: 'dream-rail', 'aria-label': 'Dreaming stages'});
  var runView = h('div', {class: 'dream-run-summary'});
  var tabs = h('div', {class: 'dream-cases', 'aria-label': 'Recorded workflows'});
  var controls = h('div', {class: 'dream-controls'});

  function clear(node) { node.replaceChildren(); }
  function badge(text) { return h('span', {class: 'dream-badge'}, text); }
  function facts(rows) {
    var list = h('dl', {class: 'dream-facts'});
    (rows || []).forEach(function (row) {
      list.appendChild(h('div', null, h('dt', null, row.label), h('dd', null, row.value)));
    });
    return list;
  }
  function stageCard(stage, index) {
    var card = h('article', {class: 'dream-card', tabindex: '-1'},
      h('div', {class: 'dream-card-top'}, h('span', {class: 'dream-step-count'}, 'Stage ' + (index + 1)), badge(stage.status)),
      h('h2', null, stage.title), h('p', {class: 'dream-summary'}, stage.summary));
    (stage.details || []).forEach(function (text) { card.appendChild(h('p', {class: 'dream-detail'}, text)); });
    if ((stage.facts || []).length) card.appendChild(facts(stage.facts));
    if ((stage.checks || []).length) {
      var list = h('ul', {class: 'dream-checks'});
      stage.checks.forEach(function (text) { list.appendChild(h('li', null, text)); });
      card.appendChild(h('section', {class: 'dream-evidence'}, h('h3', null, 'Evidence and checks'), list));
    }
    (stage.blocks || []).forEach(function (block) {
      card.appendChild(h('details', {class: 'dream-block'}, h('summary', null, block.label), h('pre', null, h('code', null, block.text))));
    });
    if ((stage.call_ids || []).length) {
      var links = h('div', {class: 'dream-call-links'}, h('span', null, 'Related calls'));
      stage.call_ids.forEach(function (id) {
        links.appendChild(h('a', {href: '#/call/' + encodeURIComponent(id)}, id));
      });
      card.appendChild(links);
    }
    return card;
  }
  function renderStages(focus) {
    var run = runs[selectedRun];
    clear(rail); clear(stageView); clear(controls);
    run.stages.forEach(function (stage, index) {
      rail.appendChild(h('button', {
        type: 'button', class: 'dream-stage-button',
        'aria-current': !showAll && index === selectedStage ? 'step' : null,
        'aria-controls': 'dream-stage-view',
        onclick: function () { selectedStage = index; showAll = false; renderStages(true); }
      }, h('span', {class: 'dream-stage-number'}, String(index + 1)),
      h('span', null, h('strong', null, stage.title), h('small', null, stage.status))));
    });
    if (showAll) run.stages.forEach(function (stage, index) { stageView.appendChild(stageCard(stage, index)); });
    else stageView.appendChild(stageCard(run.stages[selectedStage], selectedStage));
    controls.appendChild(h('button', {type: 'button', class: 'btn', 'aria-pressed': showAll,
      onclick: function () { showAll = !showAll; renderStages(false); }
    }, showAll ? 'View one stage' : 'Show all stages'));
    if (!showAll) {
      controls.appendChild(h('span', {class: 'dream-position', 'aria-live': 'polite'}, (selectedStage + 1) + ' of ' + run.stages.length));
      controls.appendChild(h('button', {type: 'button', class: 'btn', disabled: selectedStage === 0,
        onclick: function () { selectedStage--; renderStages(true); }
      }, 'Previous'));
      controls.appendChild(h('button', {type: 'button', class: 'btn btn-main', disabled: selectedStage === run.stages.length - 1,
        onclick: function () { selectedStage++; renderStages(true); }
      }, 'Next stage'));
    }
    if (focus) stageView.querySelector('article').focus({preventScroll: true});
  }
  function renderRun() {
    clear(tabs); clear(runView);
    runs.forEach(function (run, index) {
      tabs.appendChild(h('button', {type: 'button', class: 'dream-case', 'aria-pressed': index === selectedRun,
        onclick: function () { selectedRun = index; selectedStage = 0; showAll = false; renderRun(); }
      }, badge(run.status), h('strong', null, run.title), h('span', null, run.origin)));
    });
    var run = runs[selectedRun];
    runView.appendChild(h('p', {class: 'dream-run-description'}, run.summary));
    runView.appendChild(facts(run.facts));
    renderStages(false);
  }

  root.appendChild(h('header', {class: 'dream-header'},
    h('div', null, h('h1', null, 'Dreaming'), h('p', null, 'Follow an experience from call evidence to a reusable skill and its next conversation.')),
    h('div', {class: 'dream-recorded'}, 'Reviewed process record', data.recorded_at ? h('time', {datetime: data.recorded_at}, new Date(data.recorded_at).toLocaleString()) : null)));
  if (!runs.length) {
    root.appendChild(h('p', {class: 'empty'}, 'No dreaming workflows have been recorded yet. Publish a reviewed dreaming.json in the Journal notes directory.'));
    return root;
  }
  root.appendChild(tabs);
  root.appendChild(runView);
  root.appendChild(controls);
  root.appendChild(h('div', {class: 'dream-workspace'}, rail, stageView));
  root.appendChild(h('p', {class: 'dream-footnote'}, 'This view reads reviewed lifecycle records. Runtime attempts are captured automatically in each call’s Skills / MCP section. Dream and publication milestones are recorded by the operator. Refresh reloads the record.'));
  renderRun();
  return root;
};
