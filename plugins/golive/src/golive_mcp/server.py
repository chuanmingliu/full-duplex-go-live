"""Standard MCP stdio surface. stdout is reserved for MCP protocol traffic."""
from __future__ import annotations

import asyncio
import json
from typing import Annotated, Any, Literal
from uuid import UUID

from mcp.server.fastmcp import FastMCP
from pydantic import Field

from .runner import Runner

mcp = FastMCP("golive_mcp", instructions="Operate the existing Active Call route through the Mac runner. "
              "Poll returned request IDs. Never retry an uncertain dial. Wait for owner done before real-call analysis.")
READ = {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False}
QUERY = {**READ, "readOnlyHint": False, "idempotentHint": False}  # Publishes a read-only box job to disk.
CALL = {"readOnlyHint": False, "destructiveHint": False, "idempotentHint": True, "openWorldHint": True}
CallID = Annotated[str, Field(pattern=r"^[A-Za-z0-9_-]{1,80}$", description="Exact selected Call Journal call ID")]
StateHash = Annotated[str, Field(pattern=r"^[a-f0-9]{64}$", description="state_sha256 returned by a completed status operation")]


async def submit(operation: str, arguments: dict, request_id: UUID | None = None) -> dict:
    try:
        runner = Runner.configured()
        return await asyncio.to_thread(runner.submit, operation, arguments, str(request_id) if request_id else None)
    except (OSError, ValueError) as exc:
        # Configuration and guard errors are safe; OS errors can contain private filesystem details.
        raise ValueError(str(exc) if type(exc) is ValueError else "Runner unavailable; inspect local setup") from None


@mcp.tool(annotations=QUERY)
async def golive_get_status() -> dict[str, Any]:
    """Queue a status snapshot of engine, Aria binding, presets, Journal and detector.

    Returns a request_id; poll golive_get_operation for health and state_sha256.
    Does not restart any service. A Journal recording flag alone is not live recording proof.
    """
    return await submit("status", {})


@mcp.tool(annotations=QUERY)
async def golive_list_calls(offset: Annotated[int, Field(ge=0, le=100000)] = 0,
                            limit: Annotated[int, Field(ge=1, le=50)] = 20) -> dict[str, Any]:
    """Queue a bounded Journal page of IDs, build, timestamps and recording flags, without transcripts.

    Poll the returned request_id. Its result includes total and next_offset.
    """
    return await submit("list_calls", {"offset": offset, "limit": limit})


@mcp.tool(annotations=QUERY)
async def golive_list_scenarios() -> dict[str, Any]:
    """Queue deployed simulator scenario names and hashes without private personas or prompts.

    Poll request_id. Ambiguous names must be resolved privately before running a simulation.
    """
    return await submit("list_scenarios", {})


@mcp.tool(annotations=QUERY)
async def golive_get_call(call_id: CallID) -> dict[str, Any]:
    """Queue selected-call metadata, transcript counts and private LAN playback links.

    Poll the request_id. Availability is not permanent archive verification.
    Never choose a newer call implicitly. This does not perform deep analysis.
    """
    return await submit("get_call", {"call_id": call_id})


@mcp.tool(annotations=CALL)
async def golive_place_call(request_id: UUID, expected_state: StateHash,
                            mode: Literal["active_call", "sim"],
                            scenario: Annotated[str | None, Field(pattern=r"^[A-Za-z0-9_-]{1,80}$")] = None) -> dict[str, Any]:
    """Queue ONE explicitly requested Active Call or simulator call with original saved settings.

    'call' means active_call. 'call sim' means sim and requires a deployed scenario name.
    Speko/gpt-live-1 is unsupported here and must never fall back to active_call.
    Requires local allow_real_calls/allow_sim_calls setting and the latest inspected state_sha256.
    Keep one request UUID for this attempt across retries/disconnections; duplicate IDs only poll.
    There is no automatic redial. Poll the returned ID, retain its call_id, then wait for owner done.
    Preflight requires idle engine, ended previous call, presets and live 365-day recording.
    """
    if (mode == "sim") != (scenario is not None):
        raise ValueError("Simulator mode requires a scenario; real mode must omit it")
    return await submit("call", {"mode": mode, "expected_state": expected_state, "scenario": scenario}, request_id)


@mcp.tool(annotations=QUERY)
async def golive_analyze_call(call_id: CallID, owner_done: bool = False,
                              handset_estimate: bool = False) -> dict[str, Any]:
    """Queue ended-call archive verification and numeric latency analysis.

    Set owner_done only after the owner says done for this real call. It does not authorize redial.
    Reports normal responses, quotation acknowledgments/results and VAD-to-server-cut interruptions
    separately. Verifies five archive hashes, both transcript speakers and stereo signal.
    Uses the box's existing analyzer and reports its hash; strict target is 1200ms, minimum 20 turns.
    handset_estimate adds the owner's optional 50ms assumption, never acoustic verification.
    Poll request_id. Raw chat, recordings, credentials and provider logs remain on the box.
    """
    return await submit("analyze", {"call_id": call_id, "owner_done": owner_done,
                                    "handset_estimate": handset_estimate})


@mcp.tool(annotations=READ)
async def golive_get_operation(request_id: UUID) -> dict[str, Any]:
    """Read one plugin request's local receipt. Does not submit or repeat its operation.

    Returns queued, running, completed or outcome_unknown. For completed jobs check result.ok
    and data.stage; completed transport does not imply a successful or ended phone call.
    outcome_unknown means inspect this same attempt privately, never auto-redial.
    """
    try:
        return await asyncio.to_thread(Runner.configured().poll, str(request_id))
    except (OSError, ValueError) as exc:
        raise ValueError(str(exc) if type(exc) is ValueError else "Receipt unavailable; inspect local setup") from None


@mcp.resource("golive://architecture")
def architecture() -> str:
    """Active Call's control, SIP, RTP, conversation and recording paths."""
    return json.dumps({
        "control": "Mac runner -> Aria :3000 -> Active Call :8081",
        "signaling": "Active Call <-> RustPBX <-> GSM gateway (SIP INVITE, ringing, 200, ACK, BYE)",
        "media": "handset <-> carrier/GSM gateway <-> RustPBX media proxy <-> Active Call (RTP)",
        "brain": "Active Call <-> GoLive :8765/v1/realtime (WebSocket audio)",
        "conversation": "streaming ASR -> turn/focus controller -> LLM/tools -> streaming TTS -> paced player",
        "opening": "original preset pools; Talker-generated openers disabled",
        "backchannel": "optional detector assist; original ASR, focus, echo/background checks retain decisions",
        "hangup": "GoLive POST /command/{call} -> Active Call SIP BYE; caller BYE travels in reverse",
        "recording": "two-sided store -> Call Journal -> permanent archive; working retention 8760 hours",
        "transport": "MCP stdio -> fixed job -> existing Mac runner -> box; no deployment overrides",
        "experimental": "SIP/RTP adapter and Speko remain separate, unsupported call modes here"})


@mcp.resource("golive://operations")
def operations() -> str:
    """Call routing, evidence and authorization boundaries."""
    return ("call = one original Active Call; call sim = one reserved simulator call. "
            "call gpt-live-1 requires the separate Speko setup and must not fall back. "
            "Inspect status, keep request UUID and call ID, poll, wait for owner's done. "
            "Unknown dial outcomes require inspection, never automatic retries. "
            "Keep preset openers and smooth flow. Preserve echo/background rejection. "
            "Normal response and interruption latency are priorities; quotation calculation stays separate. "
            "Server output, optional +50ms estimate and acoustic handset measurement are distinct. "
            "No service restart, deployment, restore, feedback posting or raw shell tool is provided.")


@mcp.prompt()
def review_call(call_id: str) -> str:
    """Prepare an evidence-focused post-call review without initiating another call."""
    return (f"Review selected GoLive call {call_id}. Confirm the owner said done before real-call analysis. "
            "Use golive_get_call and golive_analyze_call, polling each request. Compare normal content p50/p95 "
            "and sample counts, VAD-to-cut interruptions and kept acknowledgments. Keep quotation calculations "
            "and readout separate. State archive verification and measurement boundaries. Do not redial.")


def main() -> None:
    mcp.run(transport="stdio")


if __name__ == "__main__":
    main()
