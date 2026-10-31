export const MAX_SECONDS = 15 * 60;

// The worklet owns the sample clock. Messages carry positions, so a delayed
// main thread cannot silently turn dropped audio into a shorter recording.
export class PCMEncoder {
  constructor({ maxFrames, onChunk, onStart, onIssue, onFinish, blockFrames = 4096 }) {
    if (!Number.isSafeInteger(maxFrames) || maxFrames <= 0 ||
        !Number.isSafeInteger(blockFrames) || blockFrames <= 0) throw new Error('Invalid frame limit');
    Object.assign(this, { maxFrames, onChunk, onStart, onIssue, onFinish, blockFrames });
    this.frames = 0;
    this.firstFrame = null;
    this.active = false;
    this.finished = false;
    this.buffer = new Int16Array(blockFrames);
    this.used = 0;
    this.issues = new Set();
    this.clipped = 0;
  }

  issue(code) {
    if (!this.issues.has(code)) {
      this.issues.add(code);
      this.onIssue(code);
    }
  }

  start() {
    if (!this.finished) this.active = true;
  }

  append(value) {
    if (!this.active) return;
    if (!Number.isFinite(value)) { this.issue('non_finite_input'); value = 0; }
    if (Math.abs(value) >= 1) this.clipped++;
    value = Math.max(-1, Math.min(1, value));
    this.buffer[this.used++] = Math.round(value < 0 ? value * 32768 : value * 32767);
    this.frames++;
    if (this.used === this.blockFrames) this.flush();
    if (this.frames === this.maxFrames) {
      this.issue('duration_limit_reached');
      this.stop('duration_limit');
    }
  }

  process(input, contextFrame, quantumFrames = 128) {
    if (!this.active) return;
    if (!Number.isSafeInteger(contextFrame) || contextFrame < 0) {
      this.issue('invalid_sample_clock');
      this.stop('sample_clock_error');
      return;
    }
    if (this.firstFrame === null) {
      this.firstFrame = contextFrame;
      this.onStart(contextFrame);
    }
    let skip = 0;
    const gap = contextFrame - (this.firstFrame + this.frames);
    if (gap !== 0) {
      this.issue('sample_clock_discontinuity');
      // Keep a forward gap on the timeline; it remains invalid evidence.
      for (let i = 0; i < gap && this.active; i++) this.append(0);
      skip = Math.max(0, -gap);
    }
    if (!input) this.issue('missing_input');
    const count = input ? input.length : quantumFrames;
    for (let i = skip; i < count && this.active; i++) this.append(input ? input[i] : 0);
  }

  flush() {
    if (!this.used) return;
    const samples = this.buffer.slice(0, this.used);
    this.onChunk({ start_frame: this.frames - this.used, samples });
    this.used = 0;
  }

  stop(reason = 'user') {
    if (this.finished) return;
    this.active = false;
    this.finished = true;
    this.flush();
    if (!this.frames) this.issue('empty_recording');
    this.onFinish({
      frames: this.frames,
      first_audio_context_frame: this.firstFrame,
      final_audio_context_frame: this.firstFrame === null ? null : this.firstFrame + this.frames,
      clipped_samples: this.clipped,
      stop_reason: reason,
    });
  }
}

export class CaptureBuffer {
  constructor(maxFrames) {
    this.maxFrames = maxFrames;
    this.frames = 0;
    this.chunks = [];
    this.issues = new Set();
  }

  add({ start_frame, samples }) {
    if (!Number.isSafeInteger(start_frame) || start_frame !== this.frames ||
        !(samples instanceof Int16Array) || samples.length === 0 ||
        this.frames + samples.length > this.maxFrames) {
      this.issues.add('chunk_sequence_error');
      return false;
    }
    this.chunks.push(samples);
    this.frames += samples.length;
    return true;
  }

  finish(summary) {
    if (summary.frames !== this.frames || !Number.isSafeInteger(summary.first_audio_context_frame) ||
        summary.first_audio_context_frame < 0 ||
        summary.final_audio_context_frame - summary.first_audio_context_frame !== this.frames) {
      this.issues.add('final_frame_mismatch');
    }
  }

  wav(sampleRate) {
    if (!Number.isSafeInteger(sampleRate) || sampleRate <= 0 || this.frames > (0xffffffff - 36) / 2) {
      throw new Error('Invalid WAV dimensions');
    }
    const bytes = new ArrayBuffer(44 + this.frames * 2);
    const view = new DataView(bytes);
    const text = (offset, value) => [...value].forEach((letter, i) => view.setUint8(offset + i, letter.charCodeAt(0)));
    text(0, 'RIFF'); view.setUint32(4, 36 + this.frames * 2, true); text(8, 'WAVE');
    text(12, 'fmt '); view.setUint32(16, 16, true); view.setUint16(20, 1, true);
    view.setUint16(22, 1, true); view.setUint32(24, sampleRate, true);
    view.setUint32(28, sampleRate * 2, true); view.setUint16(32, 2, true); view.setUint16(34, 16, true);
    text(36, 'data'); view.setUint32(40, this.frames * 2, true);
    let offset = 44;
    for (const chunk of this.chunks) for (const sample of chunk) { view.setInt16(offset, sample, true); offset += 2; }
    return bytes;
  }
}

export function processingIssues(settings) {
  return ['echoCancellation', 'noiseSuppression', 'autoGainControl']
    .filter(key => settings[key] !== false).map(key => 'processing_not_verified_off:' + key);
}
