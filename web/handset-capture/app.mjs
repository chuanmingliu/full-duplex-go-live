import { CaptureBuffer, MAX_SECONDS, processingIssues } from './pcm.mjs';

const el = id => document.getElementById(id);
let active = null;
let completed = null;
let metadataURL = null;

function status(text) { el('status').textContent = text; }
function seconds(frames, rate) {
  const n = frames / rate;
  return String(Math.floor(n / 60)).padStart(2, '0') + ':' + (n % 60).toFixed(1).padStart(4, '0');
}
function settingsOf(track) {
  const raw = track.getSettings();
  const result = {};
  for (const key of ['echoCancellation', 'noiseSuppression', 'autoGainControl', 'sampleRate', 'sampleSize', 'channelCount', 'latency']) {
    if (Object.hasOwn(raw, key)) result[key] = raw[key];
  }
  return result;
}
function addIssue(s, code) {
  s.issues.add(code);
  if (active === s && s.phase === 'recording') status('Recording, but capture integrity needs review: ' + [...s.issues].join(', '));
}
function clearDownloads() {
  if (completed?.wavURL) URL.revokeObjectURL(completed.wavURL);
  if (metadataURL) URL.revokeObjectURL(metadataURL);
  metadataURL = null;
  completed = null;
  el('result').hidden = true;
  el('wav').hidden = el('metadata').hidden = true;
}

function downloads() {
  if (!completed) return;
  const s = completed;
  const callID = el('call').value.trim();
  const validID = /^out-[A-Za-z0-9-]{1,80}$/.test(callID);
  el('wav').hidden = el('metadata').hidden = !validID;
  el('save-hint').textContent = validID
    ? 'Download both files. Starting another recording replaces this unsaved capture.'
    : 'Enter the matching out-… call ID to enable both downloads.';
  if (!validID) return;
  const metadata = {
    schema: 'handset-acoustic-capture/v1',
    capture_id: s.captureID,
    call_id: callID,
    wav_sha256: s.hash,
    source: 'acoustic_speakerphone_microphone',
    placement_confirmed: s.placement,
    ...s.summary,
    worklet_frames: s.summary.frames,
    sample_rate: s.ctx.sampleRate,
    frames: s.buffer.frames,
    channels: 1,
    sample_format: 'pcm16le',
    capture_complete: s.complete,
    issues: [...s.issues],
    audio_settings: s.settings,
    audio_settings_at_end: s.endSettings,
    microphone_label: s.microphone,
    requested_at: s.requestedAt,
    started_at: s.startedAt || s.requestedAt,
    ended_at: s.endedAt,
    max_frames: s.maxFrames,
  };
  // WAV hash binds these annotations to one immutable audio payload, even
  // when the call ID is filled in after dialing returns it.
  if (metadataURL) URL.revokeObjectURL(metadataURL);
  metadataURL = URL.createObjectURL(new Blob([JSON.stringify(metadata, null, 2) + '\n'], { type: 'application/json' }));
  el('wav').href = s.wavURL;
  el('wav').download = callID + '-handset.wav';
  el('metadata').href = metadataURL;
  el('metadata').download = callID + '-handset.json';
}

async function finish(s, summary) {
  if (s.finalized) return;
  s.finalized = true;
  s.phase = 'finished';
  s.endedAt = new Date().toISOString();
  s.summary = summary;
  clearInterval(s.timer);
  clearTimeout(s.stopTimer);
  navigator.mediaDevices.removeEventListener('devicechange', s.deviceChange);
  if ((s.track.getSettings().deviceId || null) !== s.deviceID) addIssue(s, 'microphone_changed');
  s.endSettings = settingsOf(s.track);
  for (const code of processingIssues(s.endSettings)) addIssue(s, code);
  s.buffer.finish(summary);
  for (const code of s.buffer.issues) addIssue(s, code);
  if (summary.stop_reason !== 'user') addIssue(s, 'stop_reason:' + summary.stop_reason);
  for (const track of s.stream.getTracks()) track.stop();
  s.source.disconnect(); s.node.disconnect(); s.gain.disconnect();
  await s.ctx.close().catch(() => {});
  try {
    const wav = s.buffer.wav(s.ctx.sampleRate);
    const digest = await crypto.subtle.digest('SHA-256', wav);
    s.hash = [...new Uint8Array(digest)].map(n => n.toString(16).padStart(2, '0')).join('');
    s.wavURL = URL.createObjectURL(new Blob([wav], { type: 'audio/wav' }));
    s.complete = s.buffer.frames > 0 && s.issues.size === 0 && summary.stop_reason === 'user';
    completed = s;
    el('result').hidden = false;
    el('integrity').textContent = s.complete
      ? 'No capture interruptions detected. Latency has not been assessed; review acoustic boundaries before using this as evidence.'
      : 'Incomplete capture evidence. Keep the files for diagnosis; they cannot pass the latency gate.';
    el('issues').textContent = [...s.issues].join('\n') +
      (summary.clipped_samples ? '\nClipped samples: ' + summary.clipped_samples + '. Review the recorded sound.' : '');
    downloads();
    el('elapsed').textContent = seconds(s.buffer.frames, s.ctx.sampleRate);
    status('Stopped. Save the WAV and metadata JSON.');
  } catch (error) {
    status('Could not prepare downloads: ' + error.message);
  } finally {
    if (active === s) active = null;
    el('start').disabled = false;
    el('stop').disabled = true;
    el('placement').disabled = false;
  }
}

function stop(reason = 'user') {
  const s = active;
  if (!s || s.phase === 'stopping' || s.finalized || !s.node) return;
  s.phase = 'stopping';
  el('stop').disabled = true;
  status('Stopping and saving the final audio samples…');
  s.node.port.postMessage({ type: 'stop', reason });
  s.stopTimer = setTimeout(() => {
    addIssue(s, 'stop_flush_timeout');
    finish(s, {
      frames: s.buffer.frames,
      first_audio_context_frame: s.firstFrame,
      final_audio_context_frame: s.firstFrame === null ? null : s.firstFrame + s.buffer.frames,
      stop_reason: 'stop_timeout',
    });
  }, 3000);
}

async function start() {
  if (active) return;
  if (!el('placement').checked) { status('Confirm the microphone and speakerphone placement before recording.'); return; }
  if (!navigator.mediaDevices?.getUserMedia || !window.AudioWorkletNode || !window.isSecureContext) {
    status('Open this page on localhost in a browser supporting microphone AudioWorklet capture.'); return;
  }
  clearDownloads();
  el('start').disabled = true;
  el('placement').disabled = true;
  el('elapsed').textContent = '00:00.0';
  status('Requesting the microphone…');
  const s = {
    captureID: crypto.randomUUID(), placement: true, phase: 'preparing',
    requestedAt: new Date().toISOString(), issues: new Set(), firstFrame: null,
  };
  active = s;
  try {
    s.stream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: false, channelCount: 1 },
      video: false,
    });
    s.track = s.stream.getAudioTracks()[0];
    s.deviceID = s.track.getSettings().deviceId || null; // Compare internally; never export raw device IDs.
    s.settings = settingsOf(s.track);
    s.microphone = s.track.label;
    for (const code of processingIssues(s.settings)) addIssue(s, code);
    if (s.track.muted) addIssue(s, 'track_muted');
    s.track.addEventListener('mute', () => { if (!s.finalized) addIssue(s, 'track_muted'); });
    s.track.addEventListener('ended', () => { if (!s.finalized) { addIssue(s, 'track_ended'); stop('track_ended'); } });
    s.deviceChange = () => {
      if (s.phase === 'recording' || s.phase === 'stopping') addIssue(s, 'audio_device_inventory_changed');
    };
    navigator.mediaDevices.addEventListener('devicechange', s.deviceChange);
    s.ctx = new AudioContext({ latencyHint: 'interactive' });
    s.maxFrames = s.ctx.sampleRate * MAX_SECONDS;
    s.buffer = new CaptureBuffer(s.maxFrames);
    await s.ctx.audioWorklet.addModule('./worklet.mjs');
    s.node = new AudioWorkletNode(s.ctx, 'handset-capture', {
      numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1],
      channelCount: 1, channelCountMode: 'explicit',
      processorOptions: { maxFrames: s.maxFrames },
    });
    s.node.port.onmessage = ({ data }) => {
      if (s.finalized) return;
      if (data.type === 'started') { s.firstFrame = data.frame; s.startedAt = new Date().toISOString(); }
      if (data.type === 'chunk') { s.buffer.add(data); s.lastChunkAt = performance.now(); }
      if (data.type === 'issue') addIssue(s, data.code);
      if (data.type === 'stopped') finish(s, data.summary);
    };
    s.node.addEventListener('processorerror', () => { addIssue(s, 'worklet_error'); stop('worklet_error'); });
    s.ctx.addEventListener('statechange', () => {
      if (s.phase === 'recording' && s.ctx.state !== 'running') {
        addIssue(s, 'audio_context_' + s.ctx.state);
        stop('audio_context_interrupted');
      }
    });
    s.source = s.ctx.createMediaStreamSource(s.stream);
    s.gain = s.ctx.createGain();
    s.gain.gain.value = 0; // Keep capture processing live without microphone feedback.
    s.source.connect(s.node); s.node.connect(s.gain); s.gain.connect(s.ctx.destination);
    await s.ctx.resume();
    if (s.ctx.state !== 'running') throw new Error('Audio context did not start');
    if (s.track.readyState !== 'live') throw new Error('Microphone ended before capture started');
    s.phase = 'recording';
    s.lastChunkAt = performance.now();
    s.node.port.postMessage({ type: 'start' });
    el('source').textContent = s.microphone + ' · ' + s.ctx.sampleRate.toLocaleString() + ' Hz · mono';
    el('stop').disabled = false;
    status(s.issues.size ? 'Recording with unverified processing settings; evidence will be marked incomplete.' : 'Recording. Keep both voices audible to this microphone.');
    s.timer = setInterval(() => {
      el('elapsed').textContent = seconds(s.buffer.frames, s.ctx.sampleRate);
      if (s.phase === 'recording' && performance.now() - s.lastChunkAt > 3000) {
        addIssue(s, 'capture_stalled'); stop('capture_stalled');
      }
    }, 250);
  } catch (error) {
    s.finalized = true;
    if (s.deviceChange) navigator.mediaDevices.removeEventListener('devicechange', s.deviceChange);
    for (const track of s.stream?.getTracks() || []) track.stop();
    await s.ctx?.close().catch(() => {});
    active = null;
    el('start').disabled = false;
    el('stop').disabled = true;
    el('placement').disabled = false;
    status('Recording did not start: ' + error.message);
  }
}

el('start').addEventListener('click', start);
el('stop').addEventListener('click', () => stop());
el('call').addEventListener('input', downloads);
window.addEventListener('beforeunload', event => {
  if (active) { event.preventDefault(); event.returnValue = ''; }
});
