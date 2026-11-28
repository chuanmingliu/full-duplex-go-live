/* The server supplies the catalog; this module never maintains a second list. */
window.GoLiveMotorCatalog = (function () {
  'use strict';
  function editor(ctx, name, body, note, msg) {
    var h = ctx.h;
    if (name !== 'quote-plans' && name !== 'quote-api') return null;
    return h('div', { class: 'row' }, h('button', {
      type: 'button', class: 'btn', text: name === 'quote-plans' ? 'Add full motor plan to draft' : 'Load Xiaocui backend mappings into draft',
      onclick: function () {
        ctx.api('GET', '/api/quote/catalog').then(function (data) {
          var draft = JSON.parse(body.value);
          if (name === 'quote-plans') {
            draft.plans = draft.plans || {};
            if (draft.plans[data.name]) throw new Error('motor-full already exists. Edit its existing definition; it has not been replaced.');
            draft.plans[data.name] = data.plan;
          } else {
            draft.coverages = Object.assign({}, draft.coverages || {}, data.quote_api.coverages);
            draft.tax_field = data.quote_api.tax_field;
          }
          body.value = JSON.stringify(draft, null, 2);
          note.value = 'Full motor catalog: reviewed Xiaocui contract';
          msg.replaceChildren(h('p', { text: 'Draft only. Review and save. The default plan, backend URL and existing customer data are unchanged. For new calls to use this plan, set default to motor-full. Backend mappings require the updated Xiaocui quotation service and rating table.' }));
        }).catch(function (e) { msg.replaceChildren(ctx.errBox(e)); });
      }
    }));
  }
  function selector(ctx, body, out) {
    var h = ctx.h, root = h('details', { class: 'card' }, h('summary', { text: 'Build a saved-plan selection' }));
    var requested;
    try { requested = JSON.parse(body.value); } catch (e) { root.appendChild(ctx.errBox(e)); return root; }
    var planName = requested.memory && requested.memory.quote_plan || '';
    ctx.api('GET', '/api/quote/selection-catalog' + (planName ? '?plan=' + encodeURIComponent(planName) : '')).then(function (data) {
      var selects = [];
      root.appendChild(h('p', { text: 'Options reflect the saved plan at load time. Reload after configuration changes. Selections remain local until Quote is pressed. Removing a main coverage requires an explicit decision about its riders.' }));
      data.entries.forEach(function (entry) {
        var select = h('select', { 'aria-label': entry.label });
        select.appendChild(h('option', { value: '', text: '不投保 / 不代缴' }));
        if (entry.kind === 'boolean') select.appendChild(h('option', { value: 'true', text: '投保' }));
        else (entry.options || []).forEach(function (option) { select.appendChild(h('option', { value: option, text: option })); });
        selects.push({ entry: entry, select: select });
        root.appendChild(h('label', { text: entry.group + ' · ' + entry.label + (entry.parent ? '（主险：' + entry.parent + '）' : '') }));
        root.appendChild(select);
      });
      root.appendChild(h('button', { class: 'btn', type: 'button', text: 'Apply selection to request', onclick: function () {
        try {
          var request = JSON.parse(body.value), selection = {};
          selects.forEach(function (row) {
            var value = row.select.value;
            if (value) selection[row.entry.name] = value === 'true' ? true : value;
            else if (row.entry.name === '车船税') selection[row.entry.name] = false;
          });
          selects.forEach(function (row) {
            if (selection[row.entry.name] && row.entry.parent && !selection[row.entry.parent]) throw new Error(row.entry.label + '需要' + row.entry.parent);
          });
          request.memory = request.memory || {};
          request.memory.quote_plan = data.name;
          request.quote_plan_version = data.version;
          request.memory.quote = selection;
          request.arguments = {};
          body.value = JSON.stringify(request, null, 2);
          out.replaceChildren(h('p', { text: 'Request updated. No quotation has been sent yet.' }));
        } catch (e) { out.replaceChildren(ctx.errBox(e)); }
      } }));
    }).catch(function (e) { root.appendChild(ctx.errBox(e)); });
    return root;
  }
  return { editor: editor, selector: selector };
}());
