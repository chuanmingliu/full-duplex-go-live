# GoLive plugin validation — 2026-11-06

The local GoLive plugin passes the related automated and read-only live checks
after two fixes identified by fault-injection tests. This report captures the
pre-push validation checkpoint on `codex/telephony-latency`.

## Test results

| Check | Result |
| --- | --- |
| Plugin suite, Python 3.11.12 | 37 passed |
| Plugin suite, Python 3.12.10 | 37 passed |
| Plugin suite, Python 3.13.14 | 37 passed |
| Existing canonical latency regression suite | 62 passed |
| Existing simulator retrospective regression suite | 10 passed |
| MCP discovery and tool schemas | 7 tools, structured output schemas present |
| MCP resources and prompts | Both resources and review prompt accessible |
| Claude plugin manifest | Passed; optional listing icon warning only |
| All three Agent Skill validators | Passed |
| Fresh ZIP dependency installation | Passed with frozen lock |
| Claude and Codex manifest MCP launch commands | Passed with actual stdio client |
| Fresh wheel install and executable | Passed outside source checkout |
| Reproducible ZIP and excluded local/private files | Passed |
| Diff whitespace check | Passed |

The manifest tests start the declared processes with plugin-relative paths
resolved as documented. They do not constitute GUI installation in Codex or
Claude, or marketplace publication. The GitHub workflow now runs the artifact
verifier. Remote CI was not part of this pre-push checkpoint; its results must
be checked against the subsequently published commit.

## Issues found and fixed

1. **Checksum enforcement depended on Python assertions.** An altered worker
   payload executed when Python ran with `-O`. The launcher now explicitly exits
   on a SHA-256 mismatch. Regression tests verify refusal both with and without
   optimization, using a harmless local marker and no phone operations.
2. **Malformed completion output broke polling.** Truncated JSON and non-object
   JSON raised exceptions. Polling now validates the result shape and returns
   `outcome_unknown` for invalid completion records. It neither returns raw
   runner output nor retries the operation.

Additional tests cover four concurrent submissions of one UUID publishing one
job, changed arguments under the same UUID being rejected, interrupted queue
publication preserving intent, disabled routes, uncertain dial outcomes, live
recording checks, archive hash failures, quote separation and interruption pairing.

## Live integration evidence

An actual MCP client submitted these read-only operations through the existing
Mac runner to the ThinkPad. Call modes remained disabled in the test configuration.

| MCP operation | Request UUID | Result |
| --- | --- | --- |
| `golive_get_status` | `c4cbc9ce-2905-4a88-bd63-4a0663205ad0` | Healthy `b51e959`, idle, original local Aria binding and presets |
| `golive_list_scenarios` | `651581ee-d465-47f3-9ac7-010ab18c2c9f` | 27 deployed scenarios |
| `golive_list_calls` | `410d7d18-72f0-49d5-bf61-f8e545b14655` | Bounded two-entry page without transcript text |
| `golive_get_call` | `56b3f4ca-622a-42c7-be55-4187baa56c23` | Exact selected-call metadata and transcript counts |
| `golive_analyze_call` | `f1e759c5-e4a0-4c7d-9bd1-cdd7990c9d6b` | Verified archived evidence and numeric analysis |

The selected call was the previously completed and owner-reviewed
`out-mvzfq218-qbqy`, not a new test call. Its five archive files verified, with
161.42 seconds of stereo audio, two channels with signal, 23 caller transcript
lines and 24 assistant lines. Results agree with the
[earlier review](2026-11-02-backchannel-real.md).

| Historical measurement | MCP result |
| --- | --- |
| Normal first-content count | 14 |
| Normal median / p95 | 919.5 / 1302 ms |
| Interruption count | 11 |
| VAD-open to server-cut median / p95 | 168 / 407 ms |
| Kept acknowledgments | 3 |
| Detector assists / fallbacks | 3 / 0 |
| Strict historical latency gate | Failed, preserved correctly |

Quotation acknowledgment, result completion and deterministic readout remain
separate fields. The existing box analyzer SHA was
`c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50`.
Server-side timings do not establish acoustic handset performance.

No real call, simulator conversation, deployment, service restart or rollback
was performed. Dialing is covered by fake-service tests. A future owner-requested
plugin-driven real call remains the acceptance test for actual phone operation.

## Reproduce

Run from `plugins/golive` in the source checkout

```bash
uv sync --frozen --group dev
PYTHONDONTWRITEBYTECODE=1 uv run --frozen pytest -q
uv build
python3 scripts/package.py
uv run --frozen python scripts/verify_dist.py
```

From the repository root, run the shared analyzer regressions

```bash
python3 -B scripts/test_call_latency.py
python3 -B scripts/test_sim_retro.py
git diff --check
```

The worker checksum and completion parsing fixes are in
`plugins/golive/src/golive_mcp/runner.py`. Regression cases are in
`plugins/golive/tests/test_runner.py`. All live receipts remain in the existing
Mac runner's private output directory; no raw conversation or audio is committed.
