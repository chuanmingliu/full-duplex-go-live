const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const entries = JSON.parse(fs.readFileSync(path.join(__dirname, '../../internal/motorcatalog/catalog.json')));
const window = {};
vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../../internal/crm/ui/motor-catalog.js'), 'utf8'), { window });
function h(tag, attrs = {}, ...children) {
  return { tag, ...attrs, value: '', children: children.flat().filter(Boolean),
    appendChild(x) { this.children.push(x); }, replaceChildren(...xs) { this.children = xs; } };
}
const flush = () => new Promise(resolve => setImmediate(resolve));
function ctx() {
  const calls = [];
  return { h, calls, errBox: e => h('error', { text: e.message }), api: async (...args) => {
    calls.push(args);
    return { name: 'motor-full', version: 7, plan: { strict: true }, entries,
      quote_api: { tax_field: 'collectVehicleTax', coverages: { '司机责任险': { code: '953' } } } };
  } };
}
test('plan draft preserves current default and requires explicit save', async () => {
  const c = ctx(), body = { value: JSON.stringify({ default: 'old', plans: { old: { custom: true } } }) }, note = {}, msg = h('div');
  const node = window.GoLiveMotorCatalog.editor(c, 'quote-plans', body, note, msg);
  node.children[0].onclick(); await flush();
  const draft = JSON.parse(body.value);
  assert.equal(draft.default, 'old'); assert.equal(draft.plans.old.custom, true);
  assert.equal(draft.plans['motor-full'].strict, true);
  assert.deepEqual(c.calls, [['GET', '/api/quote/catalog']]);
});
test('mapping draft keeps endpoint and unrelated coverage settings', async () => {
  const c = ctx(), body = { value: JSON.stringify({ base_url: 'http://fixture', coverages: { legacy: { code: 'kept' } } }) };
  const node = window.GoLiveMotorCatalog.editor(c, 'quote-api', body, {}, h('div'));
  node.children[0].onclick(); await flush();
  const draft = JSON.parse(body.value);
  assert.equal(draft.base_url, 'http://fixture'); assert.equal(draft.coverages.legacy.code, 'kept');
  assert.equal(draft.tax_field, 'collectVehicleTax');
});
test('selector preserves customer facts, requires parents, and never sends a quote', async () => {
  const c = ctx(), body = { value: JSON.stringify({ memory: { vehicleAge: 5 }, arguments: { stale: true } }) }, out = h('div');
  const node = window.GoLiveMotorCatalog.selector(c, body, out); await flush();
  const selects = node.children.filter(n => n.tag === 'select');
  assert.equal(selects.length, 18);
  const get = name => selects[entries.findIndex(e => e.name === name)];
  get('医保外用药责任险-司机').value = '共享保额';
  const button = node.children.find(n => n.tag === 'button');button.onclick();
  assert.equal(out.children[0].tag, 'error');
  get('司机责任险').value = '5万';button.onclick();
  const request = JSON.parse(body.value);
  assert.equal(request.memory.vehicleAge, 5);assert.equal(request.memory.quote_plan, 'motor-full');
  assert.equal(request.memory.quote['医保外用药责任险-司机'], '共享保额');assert.equal(request.memory.quote['车船税'], false);
  assert.deepEqual(request.arguments, {});assert.equal(request.quote_plan_version, 7);assert.deepEqual(c.calls, [['GET', '/api/quote/selection-catalog']]);
});
