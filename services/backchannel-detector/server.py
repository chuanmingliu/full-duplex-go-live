"""Local, bounded, observation-only backchannel stream service."""
import asyncio
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import time

from aiohttp import WSMsgType, web
from detector import Detector, decode_frame


class Service:
    def __init__(self, detector, capacity=4):
        self.detector = detector
        self.capacity = capacity
        self.active = 0
        self.processed = 0
        self.errors = 0
        self.slots = asyncio.Semaphore(capacity)
        self.executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="bcd")
        self.connections = set()

    async def health(self, request):
        return web.json_response({"status": "ready", "model_revision": self.detector.revision,
                                  "mode": "shadow", "default_controller": "legacy",
                                  "supported_languages": ["en"], "sample_rate": 16000,
                                  "chunk_samples": 640, "active_sessions": self.active,
                                  "capacity": self.capacity, "processed_chunks": self.processed,
                                  "errors": self.errors})

    async def infer(self, state, audio, bot):
        started = time.perf_counter()
        await asyncio.wait_for(self.slots.acquire(), timeout=.025)
        loop = asyncio.get_running_loop()

        def compute():
            at = time.perf_counter()
            result = self.detector.step(state, audio, bot)
            return result, (at - started) * 1000, (time.perf_counter() - at) * 1000

        future = loop.run_in_executor(self.executor, compute)
        # Capacity is released when computation actually ends, even if the
        # client times out/disconnects. A hung model cannot grow the work queue.
        def completed(f):
            self.slots.release()
            if not f.cancelled():
                f.exception()  # Consume errors after a timed-out request.
        future.add_done_callback(completed)
        return await asyncio.wait_for(asyncio.shield(future), timeout=.075)

    async def stream(self, request):
        if request.headers.get("Origin"):
            return web.json_response({"error": "internal_clients_only", "fallback": "legacy"}, status=403)
        if self.active >= self.capacity:
            return web.json_response({"error": "capacity", "fallback": "legacy"}, status=503)
        self.active += 1
        ws = web.WebSocketResponse(max_msg_size=8192, heartbeat=10, receive_timeout=20,
                                   compress=False)
        self.connections.add(ws)
        try:
            await ws.prepare(request)
            hello = await asyncio.wait_for(ws.receive_json(), timeout=5)
            if (not isinstance(hello, dict) or hello.get("type") != "start"
                    or type(hello.get("sample_rate")) is not int
                    or hello["sample_rate"] != 16000
                    or hello.get("language") not in ("en", "zh", "unknown")
                    or type(hello.get("aec")) is not bool):
                raise ValueError("invalid_start")
            await ws.send_json({"type": "ready", "mode": "shadow", "fallback": "legacy",
                                "control_allowed": False,
                                "language_supported": hello["language"] == "en",
                                "aec_declared": hello["aec"]})
            state = self.detector.initial_state()
            seq = 0
            opened = time.monotonic()
            async for packet in ws:
                if packet.type != WSMsgType.TEXT:
                    if packet.type in (WSMsgType.CLOSE, WSMsgType.CLOSED, WSMsgType.ERROR):
                        break
                    raise ValueError("expected_json_text")
                if seq >= 45000 or time.monotonic() - opened > 1800:
                    raise ValueError("session_limit_reconnect")
                message = json.loads(packet.data)
                audio, bot = decode_frame(message, seq)
                (state, frames), queue_ms, infer_ms = await self.infer(state, audio, bot)
                self.processed += 1
                await ws.send_json({"type": "prediction", "seq": seq, "frames": frames,
                                    "queue_ms": round(queue_ms, 3),
                                    "inference_ms": round(infer_ms, 3),
                                    "control_allowed": False, "fallback": "legacy"})
                seq += 1
        except (ValueError, TypeError, KeyError, asyncio.TimeoutError):
            self.errors += 1
            if ws.prepared and not ws.closed:
                await ws.send_json({"type": "error", "code": "invalid_or_expired_stream",
                                    "fallback": "legacy", "control_allowed": False})
        except asyncio.CancelledError:
            raise
        except Exception:
            self.errors += 1
            if ws.prepared and not ws.closed:
                await ws.send_json({"type": "error", "code": "inference_unavailable",
                                    "fallback": "legacy", "control_allowed": False})
        finally:
            self.connections.discard(ws)
            self.active -= 1
            if ws.prepared:
                await ws.close()
        return ws

    async def close(self, app):
        await asyncio.gather(*(ws.close(code=1001) for ws in list(self.connections)))
        await asyncio.to_thread(self.executor.shutdown, wait=True, cancel_futures=True)


def create_app(detector, capacity=4):
    service = Service(detector, capacity)
    app = web.Application(client_max_size=8192)
    app.router.add_get("/healthz", service.health)
    app.router.add_get("/v1/stream", service.stream)
    app.on_shutdown.append(service.close)
    return app


if __name__ == "__main__":
    detector = Detector(os.environ.get("MODEL_DIR", "/models"),
                        Path(__file__).with_name("model.lock.json"))
    web.run_app(create_app(detector), host=os.environ.get("HOST", "127.0.0.1"),
                port=int(os.environ.get("PORT", "18087")), access_log=None, print=None)
