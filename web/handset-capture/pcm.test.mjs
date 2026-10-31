import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PCMEncoder, CaptureBuffer, processingIssues } from './pcm.mjs';

function record(blocks, { maxFrames = 10000, blockFrames = 4096, reason = 'user' } = {}) {
  const received = new CaptureBuffer(maxFrames);
  const issues = [];
  const events = [];
  let summary;
  const encoder = new PCMEncoder({ maxFrames, blockFrames,
    onChunk: chunk => { events.push('chunk'); received.add(chunk); },
    onStart: () => events.push('start'), onIssue: code => issues.push(code),
    onFinish: s => { events.push('finish'); summary = s; received.finish(s); },
  });
  encoder.start();
  for (const [frame, samples, quantum] of blocks) encoder.process(samples, frame, quantum);
  encoder.stop(reason);
  return { received, issues, events, summary, encoder };
}

test('short recording flushes its exact tail before completion and exports PCM16 WAV', () => {
  const r = record([[2048, new Float32Array([-1, -0.5, 0, 0.5, 1])]]);
  assert.deepEqual(r.events, ['start', 'chunk', 'finish']);
  assert.deepEqual(r.issues, []);
  assert.equal(r.summary.frames, 5);
  assert.equal(r.summary.first_audio_context_frame, 2048);
  assert.equal(r.summary.final_audio_context_frame, 2053);
  const wav = new DataView(r.received.wav(48000));
  assert.equal(new TextDecoder().decode(new Uint8Array(wav.buffer, 0, 4)), 'RIFF');
  assert.equal(wav.getUint32(4, true), 46);
  assert.equal(wav.getUint16(22, true), 1);
  assert.equal(wav.getUint32(24, true), 48000);
  assert.equal(wav.getUint32(28, true), 96000);
  assert.equal(wav.getUint16(34, true), 16);
  assert.equal(wav.getUint32(40, true), 10);
  assert.deepEqual(Array.from({ length: 5 }, (_, i) => wav.getInt16(44 + i * 2, true)), [-32768, -16384, 0, 16384, 32767]);
  r.encoder.stop();
  r.encoder.process(new Float32Array([1]), 2053);
  assert.equal(r.received.frames, 5, 'stop is final and idempotent');
});

test('contiguous chunks and a partial final block preserve every sample', () => {
  const samples = new Float32Array(13).fill(0.25);
  const r = record([[100, samples.subarray(0, 7)], [107, samples.subarray(7)]], { blockFrames: 4 });
  assert.deepEqual(r.received.chunks.map(c => c.length), [4, 4, 4, 1]);
  assert.equal(r.received.frames, 13);
  assert.equal(r.received.issues.size, 0);
});

test('missing render frames retain their time and invalidate evidence', () => {
  const r = record([[0, new Float32Array([0.5, 0.5])], [5, new Float32Array([0.5])]]);
  assert.deepEqual(r.issues, ['sample_clock_discontinuity']);
  assert.equal(r.summary.frames, 6);
  assert.deepEqual([...r.received.chunks[0]], [16384, 16384, 0, 0, 0, 16384]);
});

test('missing input is explicit missing evidence, not a valid silent caller', () => {
  const r = record([[0, undefined, 4]]);
  assert.deepEqual(r.issues, ['missing_input']);
  assert.equal(r.summary.frames, 4);
});

test('overlapping render frames are not duplicated and remain invalid', () => {
  const r = record([[10, new Float32Array([0.25, 0.25, 0.25])], [12, new Float32Array([0.5, 0.5])]]);
  assert.deepEqual(r.issues, ['sample_clock_discontinuity']);
  assert.equal(r.summary.frames, 4);
});

test('duration cap is bounded and cannot masquerade as a complete user stop', () => {
  const r = record([[0, new Float32Array(100)]], { maxFrames: 7, blockFrames: 4 });
  assert.equal(r.summary.frames, 7);
  assert.equal(r.summary.stop_reason, 'duration_limit');
  assert.deepEqual(r.issues, ['duration_limit_reached']);
  assert.deepEqual(r.received.chunks.map(c => c.length), [4, 3]);
});

test('lost or duplicated message chunks cannot establish a valid frame sequence', () => {
  const b = new CaptureBuffer(100);
  assert.equal(b.add({ start_frame: 0, samples: new Int16Array(4) }), true);
  assert.equal(b.add({ start_frame: 8, samples: new Int16Array(4) }), false);
  b.finish({ frames: 12, first_audio_context_frame: 100, final_audio_context_frame: 112 });
  assert.deepEqual([...b.issues], ['chunk_sequence_error', 'final_frame_mismatch']);
});

test('unknown or enabled microphone processing is never interpreted as disabled', () => {
  assert.equal(processingIssues({}).length, 3);
  assert.deepEqual(processingIssues({ echoCancellation: false, noiseSuppression: false, autoGainControl: false }), []);
  assert.deepEqual(processingIssues({ echoCancellation: false, noiseSuppression: true, autoGainControl: false }), ['processing_not_verified_off:noiseSuppression']);
});

test('worklet stop message flushes the final block before reporting stopped', async () => {
  let Registered;
  const posted = [];
  globalThis.AudioWorkletProcessor = class { constructor() { this.port = { postMessage: message => posted.push(message) }; } };
  globalThis.registerProcessor = (name, Processor) => { assert.equal(name, 'handset-capture'); Registered = Processor; };
  globalThis.currentFrame = 900;
  await import('./worklet.mjs');
  const processor = new Registered({ processorOptions: { maxFrames: 48000 } });
  processor.port.onmessage({ data: { type: 'start' } });
  processor.process([[new Float32Array(128).fill(0.5)]], [[new Float32Array(128)]]);
  processor.port.onmessage({ data: { type: 'stop', reason: 'user' } });
  assert.deepEqual(posted.map(message => message.type), ['started', 'chunk', 'stopped']);
  assert.equal(posted[1].samples.length, 128);
  assert.equal(posted[2].summary.final_audio_context_frame, 1028);
  delete globalThis.AudioWorkletProcessor; delete globalThis.registerProcessor; delete globalThis.currentFrame;
});
