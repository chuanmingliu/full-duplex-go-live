// Replays two sessions' transcript events through the demo page's own row
// bookkeeping, lifted verbatim out of index.html, and checks that the second
// session starts from an empty pane.
//
// Item ids restart at item_1 / bc_1 on every socket, so anything that survives
// a reconnect gets written into by both sessions. Run with: node web/transcript_test.mjs
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, 'index.html'), 'utf8');

// --- a DOM small enough to hold a transcript -------------------------------
function makeEl(tag) {
  const el = {
    tagName: tag,
    className: '',
    _text: '',
    children: [],
    classList: {
      remove(c) { el.className = el.className.split(' ').filter((x) => x && x !== c).join(' '); },
      add(c) { if (!el.className.split(' ').includes(c)) el.className = (el.className + ' ' + c).trim(); },
    },
    appendChild(child) { el.children.push(child); return child; },
    removeChild(child) { el.children = el.children.filter((c) => c !== child); return child; },
    get firstChild() { return el.children.length ? el.children[0] : null; },
    get textContent() { return el._text; },
    set textContent(v) { el._text = String(v); if (v === '') el.children = []; },
    get innerHTML() { return el._html || ''; },
    set innerHTML(v) {
      el._html = v;
      // Only the shape row() builds: <div class="who">…</div><div class="text"></div>
      el.children = [makeEl('div'), makeEl('div')];
      el.children[0].className = 'who';
      el.children[1].className = 'text';
    },
    querySelector(sel) {
      const want = sel.replace('.', '');
      for (const c of el.children) if (c.className.split(' ').includes(want)) return c;
      return null;
    },
    scrollTop: 0,
    scrollHeight: 0,
  };
  return el;
}

const document = { createElement: makeEl };
const transcript = makeEl('div');
const ui = { transcript };

// --- the page's own bookkeeping --------------------------------------------
function lift(re, what) {
  const m = page.match(re);
  if (!m) throw new Error('could not find ' + what + ' in index.html');
  return m[0];
}
const rowsDecl = lift(/^const rows = new Map\(\);$/m, 'the rows map');
const rowFn = lift(/^function row\(id, who\) \{[\s\S]*?^\}$/m, 'function row()');
// resetTranscript() is the fix under test; absent in the version that merges.
const resetMatch = page.match(/^function resetTranscript\(\) \{[\s\S]*?^\}$/m);
const resetFn = resetMatch ? resetMatch[0] : 'function resetTranscript() { /* absent */ }';

const scope = new Function('document', 'ui', `
  ${rowsDecl}
  ${rowFn}
  ${resetFn}
  return { row, resetTranscript, rows };
`)(document, ui);

// --- replay ----------------------------------------------------------------
function session(greeting) {
  // Each socket restarts the counters, so both sessions speak into item_1.
  const r = scope.row('item_1', 'assistant');
  r.text.textContent += greeting;
}

const GREETING = '您好，我是众安保险的车险管家小翠儿。';

session(GREETING);
scope.resetTranscript(); // what connect() does on a fresh socket
session(GREETING);

const got = transcript.children.map((el) => el.querySelector('.text').textContent);
const fail = (msg) => { console.error('FAIL: ' + msg); process.exit(1); };

if (got.length !== 1) fail(`the second session should start from an empty pane; found ${got.length} rows: ${JSON.stringify(got)}`);
if (got[0] !== GREETING) fail(`the greeting was appended to the previous session's row:\n  want ${JSON.stringify(GREETING)}\n  got  ${JSON.stringify(got[0])}`);

console.log('ok — a reconnect starts the transcript over instead of writing into the last session\'s rows');

// --- the page never plays two turns at once -------------------------------
//
// The engine cuts an interrupted turn and reports it; that path works. But
// scheduling is the client's own state and it always lags the server by up to
// duplex.playback_lead_ms, so anything that leaves a turn's sources scheduled
// without a truncation arriving is heard as the previous answer continuing
// underneath the new one. Audio for a new item_id settles it on its own.
const playFn = lift(/^function playPCM\(bytes, itemID\) \{[\s\S]*?^\}$/m, 'function playPCM()');
const stopFn = lift(/^function stopPlayback\(\) \{[\s\S]*?^\}$/m, 'function stopPlayback()');

const audio = new Function('ctx', 'log', 'SESSION_RATE', `
  let pending = [];
  let playingTurn = '';
  let playhead = 0;
  ${stopFn}
  ${playFn}
  return { playPCM, stopPlayback, scheduled: () => pending.length };
`)(fakeCtx(), () => {}, 24000);

function fakeCtx() {
  let t = 0;
  return {
    state: 'running',
    get currentTime() { return t; },
    createBuffer: (_c, n) => ({ duration: n / 24000, getChannelData: () => new Float32Array(n) }),
    createBufferSource: () => ({
      buffer: null, connect() {}, start() {}, stop() {}, set onended(_f) {},
    }),
    destination: {},
  };
}

const chunk = new Uint8Array(960);
chunk.buffer.byteLength; // silence lint

audio.playPCM(chunk, 'item_1');
audio.playPCM(chunk, 'item_1');
audio.playPCM(chunk, 'item_1');
if (audio.scheduled() !== 3) fail(`three chunks of one turn should all be scheduled; got ${audio.scheduled()}`);

// The new answer starts. Whatever is still scheduled belongs to the old one.
audio.playPCM(chunk, 'item_2');
if (audio.scheduled() !== 1) {
  fail(`after a new turn's first chunk, ${audio.scheduled()} source(s) are scheduled; the ` +
    `interrupted answer is still playing underneath the new one`);
}

console.log('ok — a new turn\'s audio drops whatever the last one still had scheduled');
