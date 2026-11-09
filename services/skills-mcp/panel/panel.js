'use strict';
const el = id => document.getElementById(id);
let catalog, selected = 0, token, dirty = false;
function status(message) { el('status').textContent = message; }
async function api(method, data, path = '/admin/catalog') {
  const response = await fetch(path, {method, headers: {Authorization: 'Bearer ' + token, 'Content-Type':'application/json'}, body: data && JSON.stringify(data)});
  if (!response.ok) throw new Error((await response.text()).trim());
  return response.json();
}
function updateSkill() {
  const s = catalog.skills[selected];
  if (!s) return true;
  try { s.state = JSON.parse(el('guards').value || '{}'); s.match = JSON.parse(el('match').value || '[]'); s.exclude = JSON.parse(el('exclude').value || '[]'); }
  catch { status('State and routing rules must be valid JSON.'); return false; }
  s.events = el('events').value.split(',').map(x => x.trim()).filter(Boolean);
  s.tool = el('binding').value;
  Object.assign(s,{name:el('skillName').value, description:el('skillDesc').value, body:el('body').value, enabled:el('skillOn').checked});
  return true;
}
function renderSkills() {
  el('choose').replaceChildren(...catalog.skills.map((s,i) => new Option(s.name || 'Untitled skill', i)));
  el('choose').value = selected;
  const s = catalog.skills[selected];
  el('fields').hidden = !s;
  el('remove').disabled = !s;
  if (s) {
    el('skillName').value = s.name; el('skillDesc').value = s.description;
    el('binding').value = s.tool || ''; el('events').value = (s.events || []).join(', '); el('guards').value = JSON.stringify(s.state || {});
    el('match').value = JSON.stringify(s.match || []); el('exclude').value = JSON.stringify(s.exclude || []);
    el('body').value = s.body; el('skillOn').checked = s.enabled;
  }
}
function render() {
  const response = (catalog.event_responses || {}).addV || {enabled:false, templates:[]};
  el('addVOn').checked = response.enabled;
  el('addVTemplates').value = (response.templates || []).join('\n');
  el('toolOn').checked = catalog.quote_total.enabled;
  el('toolDesc').value = catalog.quote_total.description;
  el('timeout').value = catalog.quote_total.timeout_seconds;
  el('revision').textContent = 'Published revision ' + catalog.revision;
  renderSkills();
}
async function load() {
  if (dirty && !confirm('Discard your unpublished edits?')) return;
  catalog = await api('GET'); selected = 0; dirty = false;
  el('login').hidden = true; el('editor').hidden = false;
  render(); status('');
}
el('login').onsubmit = async e => {
  e.preventDefault(); token = el('token').value.trim();
  try { await load(); el('token').value = ''; } catch (err) { status(err.message); }
};
el('editor').oninput = e => { if (!['choose','original','event','state'].includes(e.target.id)) dirty = true; };
el('choose').onchange = () => { if (!updateSkill()) { el('choose').value = selected; return; } selected = Number(el('choose').value); renderSkills(); };
el('add').onclick = () => {
  if (!updateSkill()) return;
  selected = catalog.skills.length;
  catalog.skills.push({name:'', description:'', body:'', enabled:true}); dirty = true; renderSkills(); el('skillName').focus();
};
el('remove').onclick = () => { catalog.skills.splice(selected,1); selected = 0; dirty = true; renderSkills(); };
el('reload').onclick = () => load().catch(e => status(e.message));
el('save').onclick = async () => {
  if (!updateSkill()) return;
  const templates = el('addVTemplates').value.split('\n').map(text => text.trim()).filter(Boolean);
  if ((el('addVOn').checked && !templates.length) || templates.length > 12 || templates.some(text => [...text].length > 300) || new Set(templates).size !== templates.length) {
    status('Provide 1–12 distinct response lines when enabled, each at most 300 characters.'); return;
  }
  catalog.event_responses = {...catalog.event_responses, addV:{enabled:el('addVOn').checked, templates}};
  catalog.quote_total = {description:el('toolDesc').value, enabled:el('toolOn').checked, timeout_seconds:Number(el('timeout').value)};
  el('editor').inert = true;
  try { catalog = await api('PUT',catalog); dirty = false; render(); status('Published. The next MCP request uses this revision.'); }
  catch (err) { status(err.message); }
  finally { el('editor').inert = false; }
};

el('preview').onclick = async () => {
  try {
    const state = JSON.parse(el('state').value);
    const selection = await api('POST', {event:el('event').value, utterance:el('utterance').value, state}, '/admin/preview');
    const slots = [
      'Published revision ' + selection.revision,
      'Original prompt\n' + el('original').value,
      'Business state\n' + JSON.stringify(state),
      'Skills\n' + (selection.skills.map(s => s.name + '\n' + s.body).join('\n\n') || 'None for this turn'),
      'Available workflow tools\n' + (selection.tools.join(', ') || 'None for this turn')
    ];
    el('output').textContent = slots.join('\n\n');
  } catch (err) { el('output').textContent = err.message; }
};
