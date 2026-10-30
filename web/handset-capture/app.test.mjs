import assert from 'node:assert/strict';
import { test } from 'node:test';
import { webcrypto } from 'node:crypto';

// Exercise the browser controller with a synthetic source. These tests never
// request hardware and cannot establish acoustic latency or device behavior.
async function page(settings) {
  const elements = new Map();
  const trace = [];
  const mediaEvents = {};
  let requested;
  const track = { label: 'Synthetic microphone', readyState: 'live', muted: false,
    getSettings: () => settings, addEventListener() {}, stop() { trace.push('track-stop'); this.readyState = 'ended'; },
  };
  const element = id => {
    if (!elements.has(id)) elements.set(id, {
      value: '', checked: id === 'placement', hidden: id === 'result', listeners: {},
      addEventListener(name, handler) { this.listeners[name] = handler; },
    });
    return elements.get(id);
  };
  globalThis.document = { getElementById: element };
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: {
    mediaDevices: { addEventListener: (type, callback) => { mediaEvents[type] = callback; },
      removeEventListener: type => { delete mediaEvents[type]; },
      getUserMedia: async constraints => {
      requested = constraints;
      return { getAudioTracks: () => [track], getTracks: () => [track] };
    } },
  } });
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: webcrypto });
  globalThis.window = { isSecureContext: true, addEventListener() {} };
  class Context {
    sampleRate = 48000;
    state = 'suspended';
    destination = {};
    audioWorklet = { addModule: async () => {} };
    addEventListener() {}
    createMediaStreamSource() { return { connect() {}, disconnect() {} }; }
    createGain() { return { gain: {}, connect() {}, disconnect() {} }; }
    async resume() { this.state = 'running'; }
    async close() { trace.push('context-close'); this.state = 'closed'; }
  }
  class Node {
    constructor() {
      this.port = { postMessage: message => {
        if (message.type === 'start') queueMicrotask(() => {
          this.port.onmessage({ data: { type: 'started', frame: 512 } });
          this.port.onmessage({ data: { type: 'chunk', start_frame: 0, samples: new Int16Array([1, 2, 3]) } });
        });
        if (message.type === 'stop') queueMicrotask(() => {
          trace.push('tail');
          this.port.onmessage({ data: { type: 'chunk', start_frame: 3, samples: new Int16Array([4, 5]) } });
          this.port.onmessage({ data: { type: 'stopped', summary: {
            frames: 5, first_audio_context_frame: 512, final_audio_context_frame: 517,
            clipped_samples: 0, stop_reason: message.reason,
          } } });
        });
      } };
    }
    addEventListener() {}
    connect() {}
    disconnect() {}
  }
  globalThis.AudioContext = Context;
  globalThis.AudioWorkletNode = window.AudioWorkletNode = Node;
  await import('./app.mjs?test=' + Math.random());
  return { element, trace, mediaEvents, requested: () => requested };
}

async function completed(element) {
  const deadline = Date.now() + 2000;
  while (element('result').hidden) {
    if (Date.now() > deadline) throw new Error('Controller did not finish');
    await new Promise(resolve => setTimeout(resolve, 5));
  }
}

test('capture starts before call ID, exports exact tail and hash, and binds ID after stop', async () => {
  const p = await page({ echoCancellation: false, noiseSuppression: false, autoGainControl: false, sampleRate: 48000 });
  await p.element('start').listeners.click();
  assert.equal(p.element('call').value, '');
  assert.deepEqual(p.requested().audio, { echoCancellation: false, noiseSuppression: false, autoGainControl: false, channelCount: 1 });
  p.element('stop').listeners.click();
  await completed(p.element);
  assert.deepEqual(p.trace, ['tail', 'track-stop', 'context-close']);
  assert.equal(p.element('metadata').hidden, true);
  p.element('call').value = 'out-synthetic-123';
  p.element('call').listeners.input();
  const metadata = await (await fetch(p.element('metadata').href)).json();
  const wav = await (await fetch(p.element('wav').href)).arrayBuffer();
  assert.equal(metadata.capture_complete, true);
  assert.equal(metadata.stop_reason, 'user');
  assert.equal(metadata.frames, 5);
  assert.equal(metadata.call_id, 'out-synthetic-123');
  assert.equal(metadata.final_audio_context_frame - metadata.first_audio_context_frame, 5);
  assert.equal(wav.byteLength, 54);
  const digest = await webcrypto.subtle.digest('SHA-256', wav);
  assert.equal(metadata.wav_sha256, Buffer.from(digest).toString('hex'));
  p.element('call').value = 'out-synthetic-456';
  p.element('call').listeners.input();
  const rebound = await (await fetch(p.element('metadata').href)).json();
  assert.equal(rebound.capture_id, metadata.capture_id);
  assert.equal(rebound.started_at, metadata.started_at);
  assert.equal(rebound.wav_sha256, metadata.wav_sha256);
  assert.equal(rebound.call_id, 'out-synthetic-456');
  URL.revokeObjectURL(p.element('metadata').href); URL.revokeObjectURL(p.element('wav').href);
});

test('missing actual processing flags remain unknown and cannot create complete evidence', async () => {
  const p = await page({ sampleRate: 48000 });
  await p.element('start').listeners.click();
  p.element('stop').listeners.click();
  await completed(p.element);
  p.element('call').value = 'out-synthetic-unknown'; p.element('call').listeners.input();
  const metadata = await (await fetch(p.element('metadata').href)).json();
  assert.equal(metadata.capture_complete, false);
  assert.equal(metadata.issues.length, 3);
  assert.equal(Object.hasOwn(metadata.audio_settings, 'echoCancellation'), false);
  URL.revokeObjectURL(p.element('metadata').href); URL.revokeObjectURL(p.element('wav').href);
});

test('device change invalidates evidence and listener is removed on completion', async () => {
  const p = await page({ echoCancellation: false, noiseSuppression: false, autoGainControl: false });
  await p.element('start').listeners.click();
  p.mediaEvents.devicechange();
  p.element('stop').listeners.click();
  await completed(p.element);
  p.element('call').value = 'out-synthetic-change'; p.element('call').listeners.input();
  const metadata = await (await fetch(p.element('metadata').href)).json();
  assert.equal(metadata.capture_complete, false);
  assert.deepEqual(metadata.issues, ['audio_device_inventory_changed']);
  assert.equal(p.mediaEvents.devicechange, undefined);
  URL.revokeObjectURL(p.element('metadata').href); URL.revokeObjectURL(p.element('wav').href);
});

test('silent input-device replacement is detected without exporting device IDs', async () => {
  const settings = { echoCancellation: false, noiseSuppression: false, autoGainControl: false, deviceId: 'original-private-id' };
  const p = await page(settings);
  await p.element('start').listeners.click();
  settings.deviceId = 'replacement-private-id';
  p.element('stop').listeners.click();
  await completed(p.element);
  p.element('call').value = 'out-synthetic-replacement'; p.element('call').listeners.input();
  const metadata = await (await fetch(p.element('metadata').href)).json();
  assert.equal(metadata.capture_complete, false);
  assert.deepEqual(metadata.issues, ['microphone_changed']);
  assert.equal(JSON.stringify(metadata).includes('private-id'), false);
  URL.revokeObjectURL(p.element('metadata').href); URL.revokeObjectURL(p.element('wav').href);
});
