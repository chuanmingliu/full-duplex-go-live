import asyncio
import base64
import json
import os
from pathlib import Path
import time
import unittest

from aiohttp import WSServerHandshakeError
from aiohttp.test_utils import TestClient, TestServer
import numpy as np

from detector import Detector, decode_frame
from server import create_app


def packet(seq=0):
    return {"type": "audio", "seq": seq, "pcm_s16le": base64.b64encode(bytes(1280)).decode(),
            "bot_state": [[1, 0, 0, 1, 0, 0, 0, 0]] * 2}


class Fake:
    revision = "test"
    delay = 0

    def initial_state(self):
        return 0

    def step(self, state, audio, bot):
        time.sleep(self.delay)
        return state + 1, [{"test_state": state + 1}] * 2


class ProtocolTests(unittest.TestCase):
    def test_audio_is_little_endian_normalized(self):
        p = packet()
        p["pcm_s16le"] = base64.b64encode(np.full(640, -32768, dtype="<i2").tobytes()).decode()
        a, b = decode_frame(p, 0)
        self.assertTrue((a == -1).all())
        self.assertEqual(b.shape, (1, 2, 8))

    def test_bad_frame_does_not_enter_model(self):
        for edit in ({"seq": 1}, {"seq": True}, {"pcm_s16le": "!"},
                     {"pcm_s16le": ""}, {"bot_state": [[float("nan")] * 8] * 2},
                     {"bot_state": [[1] * 8] * 2}, {"bot_state": [[0] * 8]}):
            with self.subTest(edit=edit), self.assertRaises(ValueError):
                decode_frame({**packet(), **edit}, 0)


class StreamTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.model = Fake()
        self.client = TestClient(TestServer(create_app(self.model, capacity=2)))
        await self.client.start_server()

    async def asyncTearDown(self):
        await self.client.close()

    async def connect(self):
        ws = await self.client.ws_connect("/v1/stream")
        await ws.send_json({"type": "start", "sample_rate": 16000, "language": "zh", "aec": False})
        ready = await ws.receive_json()
        self.assertFalse(ready["control_allowed"])
        self.assertFalse(ready["language_supported"])
        self.assertEqual(ready["fallback"], "legacy")
        return ws

    async def test_state_isolation_and_default_legacy(self):
        a, b = await self.connect(), await self.connect()
        for ws, seq, expected in [(a, 0, 1), (a, 1, 2), (b, 0, 1)]:
            await ws.send_json(packet(seq))
            result = await ws.receive_json()
            self.assertEqual(result["frames"][0]["test_state"], expected)
            self.assertFalse(result["control_allowed"])
        await a.close(); await b.close()

    async def test_sequence_gap_and_malformed_audio_fail_to_legacy(self):
        for value in [packet(1), {**packet(), "pcm_s16le": "!"}]:
            ws = await self.connect()
            await ws.send_json(value)
            result = await ws.receive_json()
            self.assertEqual(result["type"], "error")
            self.assertEqual(result["fallback"], "legacy")
            await ws.close()

    async def test_overload_is_bounded(self):
        a, b = await self.connect(), await self.connect()
        with self.assertRaises(WSServerHandshakeError) as caught:
            await self.client.ws_connect("/v1/stream")
        self.assertEqual(caught.exception.status, 503)
        await a.close(); await b.close()

    async def test_model_deadline_does_not_block_health(self):
        self.model.delay = .2
        ws = await self.connect()
        await ws.send_json(packet())
        result = await ws.receive_json()
        self.assertEqual(result["type"], "error")
        health = await self.client.get("/healthz")
        self.assertEqual(health.status, 200)
        self.assertEqual((await health.json())["default_controller"], "legacy")
        await ws.close()

    async def test_browser_origin_rejected(self):
        with self.assertRaises(WSServerHandshakeError) as caught:
            await self.client.ws_connect("/v1/stream", headers={"Origin": "https://example.com"})
        self.assertEqual(caught.exception.status, 403)


@unittest.skipUnless(os.environ.get("BCD_MODEL_DIR"), "set BCD_MODEL_DIR for real ONNX smoke")
class ModelTests(unittest.TestCase):
    def test_real_model_reset_reproduces_initial_prediction(self):
        model = Detector(os.environ["BCD_MODEL_DIR"], Path(__file__).with_name("model.lock.json"))
        audio, bot = decode_frame(packet(), 0)
        state, first = model.step(model.initial_state(), audio, bot)
        self.assertNotEqual(model.step(state, audio, bot)[1], first)
        self.assertEqual(model.step(model.initial_state(), audio, bot)[1], first)
        self.assertEqual(len(first), 2)


if __name__ == "__main__":
    unittest.main()
