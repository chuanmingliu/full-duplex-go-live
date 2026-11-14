"""Pinned streaming ONNX inference. No transcript, recording or control action."""
import base64
import hashlib
import json
from pathlib import Path

import numpy as np
import onnxruntime as ort

LABELS = ["continuer", "assessment", "negative", "floor_take", "non_speech"]


def decode_frame(message, expected_seq):
    if not isinstance(message, dict) or message.get("type") != "audio":
        raise ValueError("expected_audio")
    if type(message.get("seq")) is not int or message["seq"] != expected_seq:
        raise ValueError("sequence_gap_reconnect")
    encoded = message.get("pcm_s16le", "")
    if not isinstance(encoded, str) or len(encoded) > 1712:
        raise ValueError("invalid_audio")
    try:
        raw = base64.b64decode(encoded, validate=True)
    except (ValueError, TypeError):
        raise ValueError("invalid_audio") from None
    if len(raw) != 1280:
        raise ValueError("expected_640_pcm16_samples")
    try:
        bot = np.asarray(message.get("bot_state"), dtype=np.float32)
    except (ValueError, TypeError, OverflowError):
        raise ValueError("invalid_bot_state") from None
    if (bot.shape != (2, 8) or not np.isfinite(bot).all()
            or (bot < 0).any() or (bot > 1).any()
            or (bot[:, 5:] != 0).any()
            or not np.isin(bot[:, [0, 4]], [0, 1]).all()):
        raise ValueError("invalid_bot_state")
    audio = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
    return audio.reshape(1, 640), bot.reshape(1, 2, 8)


class Detector:
    def __init__(self, directory, lock_path):
        lock = json.loads(Path(lock_path).read_text())
        for name, digest in lock["files"].items():
            if hashlib.sha256((Path(directory) / name).read_bytes()).hexdigest() != digest:
                raise ValueError("model_checksum_mismatch")
        config = json.loads((Path(directory) / "model.json").read_text())
        if config["chunk_samples"] != 640 or config["frames_per_chunk"] != 2:
            raise ValueError("unsupported_model_shape")
        options = ort.SessionOptions()
        options.intra_op_num_threads = 1
        options.inter_op_num_threads = 1
        ort.disable_telemetry_events()
        self.session = ort.InferenceSession(
            str(Path(directory) / "backchannel-fp32.onnx"), options,
            providers=["CPUExecutionProvider"],
        )
        self.revision = lock["revision"]
        self.state_names = config["state_names"]
        inputs = {x.name: x for x in self.session.get_inputs()}
        self.shapes = {name: inputs[name].shape for name in self.state_names}
        self.outputs = [x.name for x in self.session.get_outputs()]
        self.step(self.initial_state(), np.zeros((1, 640), np.float32),
                  np.zeros((1, 2, 8), np.float32))

    def initial_state(self):
        return {name: np.zeros(shape, np.float32) for name, shape in self.shapes.items()}

    def step(self, state, audio, bot):
        output = dict(zip(self.outputs, self.session.run(
            None, {**state, "chunk": audio, "bot_state": bot})))
        intent, vad, vap = output["intent"], output["vad"], output["vap"]
        if intent.shape != (1, 2, 5) or vad.shape != (1, 2) or vap.shape != (1, 2, 8):
            raise ValueError("invalid_model_output")
        for value in (intent, vad, vap):
            if not np.isfinite(value).all() or (value < 0).any() or (value > 1).any():
                raise ValueError("invalid_model_output")
        if not np.allclose(intent.sum(axis=-1), 1, atol=1e-3):
            raise ValueError("invalid_model_probabilities")
        next_state = {name: output[name + "_out"] for name in self.state_names}
        if any(not np.isfinite(v).all() for v in next_state.values()):
            raise ValueError("invalid_model_state")
        frames = []
        for i in range(2):
            p = intent[0, i]
            frames.append({"intent": dict(zip(LABELS, map(float, p))),
                           "p_backchannel": float(p[0] + p[1]),
                           "p_floor": float(p[2] + p[3]),
                           "vad": float(vad[0, i]), "vap": vap[0, i].tolist()})
        return next_state, frames
