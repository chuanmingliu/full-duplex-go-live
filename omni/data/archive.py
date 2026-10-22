"""Read an archived golive call into training records.

The archive (internal/journal/archive.go) keeps one directory per call:
audio.wav (stereo, caller left, assistant right), call.json (the journal's
reading of the log: turns, transcript, utterances, marks), and the prompt
under prompts/<sha>.txt. This module turns one such directory into

- turn records: the caller's words, the answer as it was heard, the prompt
  and the prior lines — what a thinker/talker fine-tune trains on;
- floor labels: one label per 80 ms frame of the whole call — what a duplex
  head trains on.

Standard library only, so it runs on the box's Python 3.14 as it is.
Nothing here prints what was said.
"""
from __future__ import annotations

import json
import struct
import wave
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Iterator

FRAME_MS = 80
MIN_CALLER_MS = 200

SILENCE, LISTEN, WAIT, SPEAK, YIELD, HOLD = (
    "silence", "listen", "wait", "speak", "yield", "hold")


@dataclass
class Segment:
    start_s: float
    end_s: float
    text: str
    wav: str = ""
    interrupted: bool = False
    cache_hit: bool = False


@dataclass
class TurnRecord:
    call_id: str
    turn: int
    sim: bool
    scenario: str
    build: str
    prompt_sha: str
    caller: Segment
    assistant: Segment
    history: list[dict] = field(default_factory=list)
    latency_ms: int = 0

    def to_json(self) -> dict:
        d = asdict(self)
        return d


@dataclass
class Dropped:
    call_id: str
    turn: int
    reason: str


@dataclass
class Audio:
    """Both tracks of a recording as int16 samples."""
    rate: int
    caller: list[int]
    assistant: list[int]

    @property
    def seconds(self) -> float:
        return len(self.caller) / self.rate

    def cut(self, track: list[int], start_s: float, end_s: float) -> list[int]:
        a = max(0, int(start_s * self.rate))
        b = min(len(track), int(end_s * self.rate))
        return track[a:b] if b > a else []


def read_wav(path: Path) -> Audio:
    with wave.open(str(path), "rb") as w:
        rate, ch, width, n = w.getframerate(), w.getnchannels(), w.getsampwidth(), w.getnframes()
        raw = w.readframes(n)
    if width != 2:
        raise ValueError(f"{path}: {width * 8}-bit samples; the archive writes 16-bit")
    samples = struct.unpack(f"<{len(raw) // 2}h", raw)
    if ch == 2:
        return Audio(rate, list(samples[0::2]), list(samples[1::2]))
    if ch == 1:
        return Audio(rate, list(samples), [0] * len(samples))
    raise ValueError(f"{path}: {ch} channels")


def write_wav(path: Path, rate: int, samples: list[int]) -> int:
    path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(struct.pack(f"<{len(samples)}h", *samples))
    return path.stat().st_size


def realign(audio: Audio, marks: list[dict]) -> Audio:
    """Undo the caller track running ahead of the clock (ISS-030).

    Older recordings appended the caller's chunks as they arrived, and on
    evening calls they arrived 2-3% faster than time passed, so the caller's
    side ends several seconds late against the assistant's. The journal's
    player drops repeats of the previous chunk while the track is ahead
    (Recording.Realign in internal/journal); this does the same from the
    audio_in marks: for each span between marks, the track is stretched to
    the clock by dropping the chunks that repeat their predecessor.
    """
    if len(marks) < 2:
        return audio
    chunk = max(1, (marks[-1]["bytes"] // 2) if marks[-1].get("bytes") else 320)
    out: list[int] = []
    pos = 0
    prev_mark = marks[0]
    for m in marks[1:]:
        frames = m["frames"] - prev_mark["frames"]
        want = int((m["t_s"] - prev_mark["t_s"]) * audio.rate)
        span = audio.caller[pos:pos + frames * chunk]
        pos += frames * chunk
        if len(span) <= want:
            out.extend(span)
        else:
            excess = len(span) - want
            kept: list[int] = []
            last = None
            for i in range(0, len(span), chunk):
                c = span[i:i + chunk]
                if excess >= len(c) and last is not None and c == last:
                    excess -= len(c)
                    continue
                kept.extend(c)
                last = c
            out.extend(kept)
        prev_mark = m
    out.extend(audio.caller[pos:])
    return Audio(audio.rate, out, audio.assistant)


def load_call(call_dir: Path) -> tuple[dict, Audio | None]:
    call = json.loads((call_dir / "call.json").read_text())
    wav = call_dir / "audio.wav"
    audio = read_wav(wav) if wav.exists() else None
    if audio is not None and call.get("audio_in"):
        audio = realign(audio, call["audio_in"])
    return call, audio


def assistant_segments(call: dict) -> dict[int, Segment]:
    """The answer of each turn as it was heard: from its first audio to the
    next line of the conversation, or the caller's next start."""
    lines = sorted(call.get("transcript", []), key=lambda s: s["t_s"])
    segs: dict[int, Segment] = {}
    turns = {t["n"]: t for t in call.get("turns", [])}
    for i, s in enumerate(lines):
        if s.get("who") != "assistant" or not s.get("turn"):
            continue
        t = turns.get(s["turn"])
        if t is None:
            continue
        # An unanswered caller line (a murmur, a hesitation) does not end the
        # answer unless the answer was cut: then the first sound over it does.
        end = call.get("duration_s", s["t_s"] + 30)
        cut = bool(t.get("interrupted"))
        for nxt in lines[i + 1:]:
            if nxt.get("who") == "assistant":
                end = nxt["t_s"]
                break
            if nxt.get("who") == "caller" and (nxt.get("turn") or cut):
                end = nxt.get("start_s") or nxt["t_s"]
                break
        segs[s["turn"]] = Segment(
            start_s=t.get("audio_out_s", s["t_s"]), end_s=max(end, s["t_s"]),
            text=s.get("text", ""), interrupted=bool(t.get("interrupted")),
            cache_hit=bool(t.get("cache_hit")))
    return segs


def caller_segments(call: dict) -> dict[int, Segment]:
    segs: dict[int, Segment] = {}
    turns = {t["n"]: t for t in call.get("turns", [])}
    for s in call.get("transcript", []):
        if s.get("who") != "caller" or not s.get("turn"):
            continue
        t = turns.get(s["turn"])
        if t is None:
            continue
        end = t.get("last_sound_s", s["t_s"])
        start = s.get("start_s") or max(0.0, end - 8.0)
        segs[s["turn"]] = Segment(start_s=start, end_s=end, text=s.get("text", ""))
    return segs


def turn_records(call: dict, *, sim: bool, scenario: str,
                 history: int = 6) -> tuple[list[TurnRecord], list[Dropped]]:
    callers, answers = caller_segments(call), assistant_segments(call)
    lines = sorted(call.get("transcript", []), key=lambda s: s["t_s"])
    out: list[TurnRecord] = []
    dropped: list[Dropped] = []
    cid = call["id"]
    for t in sorted(call.get("turns", []), key=lambda t: t["n"]):
        n = t["n"]
        c, a = callers.get(n), answers.get(n)
        if c is None or not c.text.strip():
            dropped.append(Dropped(cid, n, "no caller text"))
            continue
        if a is None or not a.text.strip():
            dropped.append(Dropped(cid, n, "no assistant text"))
            continue
        if t.get("last_sound_s", -1) < 0 or t.get("audio_out_s", -1) < 0:
            dropped.append(Dropped(cid, n, "unplaced"))
            continue
        if (c.end_s - c.start_s) * 1000 < MIN_CALLER_MS:
            dropped.append(Dropped(cid, n, "caller segment too short"))
            continue
        prior = [{"who": s["who"], "text": s.get("text", "")}
                 for s in lines if s["t_s"] < c.start_s and s.get("text")]
        out.append(TurnRecord(
            call_id=cid, turn=n, sim=sim, scenario=scenario,
            build=call.get("build", ""), prompt_sha=call.get("prompt_sha", ""),
            caller=c, assistant=a, history=prior[-history:],
            latency_ms=int(t.get("total_ms", 0))))
    return out, dropped


def floor_labels(call: dict, duration_s: float | None = None) -> list[str]:
    """One label per FRAME_MS frame of the call."""
    dur = duration_s or call.get("duration_s") or 0.0
    n = int(dur * 1000 / FRAME_MS) + 1
    labels = [SILENCE] * n
    answers = assistant_segments(call)
    turns = {t["n"]: t for t in call.get("turns", [])}

    def frames(a: float, b: float) -> range:
        return range(max(0, int(a * 1000 / FRAME_MS)), min(n, int(b * 1000 / FRAME_MS) + 1))

    # The assistant's voice.
    for seg in answers.values():
        for i in frames(seg.start_s, seg.end_s):
            labels[i] = SPEAK
    # The caller's voice, from the gate's own record. Over the assistant it is
    # a yield if the answer stopped, a hold if it carried on.
    for u in call.get("utterances", []):
        a, b = u["start_s"], u.get("end_s") or u["start_s"] + 0.3
        over = u.get("barge_in", False)
        cut = False
        if over:
            for seg in answers.values():
                if seg.start_s <= a <= seg.end_s and seg.interrupted:
                    cut = True
                    break
        for i in frames(a, b):
            if labels[i] == SPEAK or over:
                labels[i] = YIELD if cut else HOLD
            else:
                labels[i] = LISTEN
    # The wait: the caller's last sound to the answer's first audio.
    for n_, t in turns.items():
        a, b = t.get("last_sound_s", -1), t.get("audio_out_s", -1)
        if a < 0 or b < 0 or b <= a:
            continue
        for i in frames(a, b):
            if labels[i] == SILENCE:
                labels[i] = WAIT
    return labels


def iter_calls(archive: Path) -> Iterator[Path]:
    for p in sorted(archive.glob("calls/*/*/*/call.json")):
        yield p.parent
