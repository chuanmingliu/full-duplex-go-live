import { PCMEncoder } from './pcm.mjs';

class HandsetCapture extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.encoder = new PCMEncoder({
      maxFrames: options.processorOptions.maxFrames,
      onChunk: chunk => this.port.postMessage({ type: 'chunk', ...chunk }, [chunk.samples.buffer]),
      onStart: frame => this.port.postMessage({ type: 'started', frame }),
      onIssue: code => this.port.postMessage({ type: 'issue', code }),
      onFinish: summary => this.port.postMessage({ type: 'stopped', summary }),
    });
    this.port.onmessage = ({ data }) => {
      if (data.type === 'start') this.encoder.start();
      if (data.type === 'stop') this.encoder.stop(data.reason);
    };
  }

  process(inputs, outputs) {
    this.encoder.process(inputs[0]?.[0], currentFrame, outputs[0]?.[0]?.length || 128);
    return true; // Keep the port alive until the final tail has been received.
  }
}
registerProcessor('handset-capture', HandsetCapture);
