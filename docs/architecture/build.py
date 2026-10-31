#!/usr/bin/env python3
"""Regenerate architecture guides and the offline explorer. No network or dependencies."""
import argparse
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent

DIAGRAMS = {
    'call': '''flowchart LR
  Mac[Mac runner] -->|dial request| Aria[Aria originator]
  Aria -->|setup| Active[Active Call]
  Aria -->|opening| Go[golive]
  Phone[Handset] <-->|carrier| GSM[GSM gateway]
  GSM <-->|SIP| PBX[RustPBX]
  PBX <--> Active
  Active <-->|Realtime WebSocket| Go
  Active <-.->|separate test route| Sim[Synthetic customer gateway]''',
    'engine': '''flowchart LR
  Audio[Continuous audio] --> Listen[Listen / VAD]
  Listen --> ASR[Tencent ASR]
  Listen --> Focus[Caller verification]
  ASR --> Turn[Turn tracker]
  Focus --> Turn
  Turn --> LLM[DeepSeek]
  LLM --> Segment[Speech segmenter]
  Segment --> TTS[MiniMax / fallback]
  TTS --> Player[Paced player]
  Turn -->|interrupt / ownership| Player
  Turn --> Cache[Opener cache]
  Cache --> Player
  Player --> Out[Output audio / transcript / timing]''',
    'business': '''flowchart LR
  Config[Templates / plans / rules] --> CRM[CRM adapter]
  LLM[Reasoning] --> Tools[Async tools]
  Memory[Versioned call memory] <--> Tools
  Tools <-->|quote_total| CRM
  CRM <--> Local[Local quote service]
  CRM <-.-> Business[Configured intranet API]
  CRM --> Prepared[Prepared results]
  Prepared --> PCM[Bounded first-segment audio]
  PCM --> Player[Player]
  Tools -->|ready result| Player
  Tools -->|result and spoken context| LLM''',
    'evidence': '''flowchart LR
  Engine[golive] --> Store[Working session store]
  Engine --> Logs[Output / timing logs]
  Store --> Journal[Call Journal]
  Logs --> Journal
  Journal <--> Archive[Permanent archive + hashes]
  Journal --> Desk[Desk call views]
  Phone[Physical handset + caller] --> Capture[One-clock acoustic recording]
  Capture --> Acoustic[Handset latency analysis]
  Logs --> Server[Separate server latency analysis]''',
    'delivery': '''flowchart LR
  Dev[Mac source / tests] --> GH[GitHub PR]
  GH --> CI[Quality + normal + race]
  CI -->|verified commit| Build[Identifiable release bundle]
  Build --> Runner[Mac runner / SSH]
  Runner --> Idle[Idle check + preserve state]
  Idle --> Box[Replace required box services]
  Box --> Verify[Health / hashes / warming / archive]
  Verify --> Replay[Matched replay + sanitized notes]''',
    'lab': '''flowchart LR
  Desk[Desk] <-->|scenario / report| Sim[Simulator]
  Sim <-->|isolated gateway| Go[golive phone engine]
  Desk <-->|prompt / streamed clause| Talker[Mac Talker]
  Journal[Journal] -->|health probe only| Talker
  Probes[Diagnostic tools] <--> Go
  Probes <--> Edge[Local model gateway]
  Go <-->|configured focus path| TSV[Target-speaker verification]''',
}

EXTRAS = {
 'call': '''### Two entry points, one engine

The real phone path uses `/v1/realtime` because the media gateway speaks the OpenAI Realtime shape. Native clients can use `/v1/live/sessions`, with optional sideband attach, stored-session fork and recording content routes. Audio flows continuously; the server handles turn timing. SIP signalling is not the same transport as the audio WebSocket.

`call` uses the saved prompt and configured real recipient. `call sim` targets a reserved simulator run through its isolated gateway. Preserve call IDs throughout collection; a newer call must never silently replace the selected one.
''',
 'engine': '''[Detailed ASR → LLM → TTS walkthrough](07-streaming-coordination.md): overlap, commitment, backpressure, revisions, interruptions and timing.

### What “full duplex” means here

Listen, transcribe, think and speak can overlap. Speculative reasoning can begin during a pause before the turn is committed. When the caller resumes, stale work must be cancelled or detached from audible playback. The engine tracks which turn and revision actually owns each piece of speech.

The current profile enables phrase-cache openers, speculative work, caller focus and interruption. It disables periodic general backchannels and holding fillers. An opener can still be reclassified as a backchannel when the caller continues; those are separate concepts. The Mac Talker experiment is not the source of these cached phrases.

### Two delegation modes

In `responses` mode, golive calls the configured backend model and translates its responses/tools into the session event contract. In `client` mode, an application receives delegation events and returns commentary or thinking context. The checked phone profile uses `responses`. Supporting an OpenAI-shaped protocol does not make this a native OpenAI realtime speech model.

### Timing and capacity

The checked profile has a 380 ms base silence threshold, 120 ms early-final request, eight requested LLM hedge copies, four-character initial text chunk and 40 ms playback chunks. These are configuration values, not independent delays to add together: much of the work overlaps. Provider account pools, rate limits and service conditions affect the actual result.
''',
 'business': '''### Ordinary quotation path

1. The caller's request enters the model and tool coordinator.
2. `quote_total` reads the current plan and facts. Missing essential fields return a structured `need` result for a follow-up question.
3. A compatible ready result is reused, or the CRM calls the configured pricing implementation.
4. Slow work returns asynchronously with its task and caller origins preserved.
5. Deterministic speech reads the quotation; the backend receives context about what was already spoken so it can continue naturally without repeating it.

### What gets prepared

The CRM prepares the default quote while the phone rings, and can prepare a bounded set of nearby selections. The engine separately prepares the first audio segment of a bounded number of eligible results. Neither cache is a substitute for validating call identity, pricing configuration, facts, voice identity and expiry.

The local quotation service implements the intranet API contract with placeholder tables and intentional delay. Switching `quote-api.base_url` to a business service changes the adapter destination; the diagram does not assert that a production insurer connection is active.
''',
 'evidence': '''### The three latency answers

| Measure | End boundary | What it establishes |
| --- | --- | --- |
| First audio | First server output audio, including an opener | The application emitted sound |
| First content / quote readout | Answer content or deterministic quote output | The application started the relevant response |
| Physical handset | Audible response at the real phone after caller speech ends | End-to-end telephony experience |

The latest reviewed real call, `out-mvvvt9l4-cauj` on `d85232a`, had 16 measured caller samples: first-audio p95 653 ms and first-content p95 1313 ms at the server boundary. These observations are historical, not a measurement of the later `f842928` release, and do not establish physical handset latency. Sixteen samples also falls short of the strict twenty-sample acceptance minimum.

The target remains p95 strictly below 1200 ms. Do not pool different builds, simulator calls and real calls to manufacture a passing result. Interrupted answers that actually emitted content remain relevant. Missing evidence, ambiguous handoffs and absent output must remain visible.

### Evidence locations and privacy

Working PCM and session metadata live under `~/golive-calls`. Ended-call evidence is retained under `~/golive-archive`; the Journal presents that evidence. Desk reads through to it and stores its own notes/work logs separately under `~/golive-desk`. Repository retrospectives are sanitized explanations; they are not a replacement for the private audio and manifest.
''',
 'delivery': '''### Release lifecycle

The repository workflow triggers on pull requests and pushes to `dev`, `master` and `telephony-realtime`. The task branch receives checks through its PR. GitHub Actions runs quality, normal tests and race tests; the workflow contains no direct deployment stage. The agent-operated runner workflow supplies continuous delivery for this setup.

Before replacing a service, establish that the main phone and browser demo are idle. Preserve private environment files, prompt/greeting, voice, business configuration, retained archives and working stores. Retain the actual prior binaries and changed files for rollback. Confirm the running binary's identity after restart rather than relying solely on the bundle's version label.

CRM is an independent long-running process and is not automatically replaced by a normal phone-service restart. Journal metadata changes can be picked up from disk without restarting a call. Restarting components is different from deploying note files.

### Access boundaries

The checked phone profile listens on localhost and disables its own authentication there. Production profiles support session authentication, caps and backpressure. CRM and Desk have token-protected APIs. Journal does not provide its own login. Public tunnel and Access configuration are separate infrastructure, so the documented hostname table is not a current security or DNS audit.
''',
 'lab': '''### How to interpret each test

| Tool | Exercises | Does not prove |
| --- | --- | --- |
| Unit/race tests | Logic, concurrency and regressions | Provider performance or telephone quality |
| Provider probes | A model/synthesizer boundary | Complete conversational experience |
| Loopback benchmark | Application/provider pipeline | Carrier and handset transport |
| Synthetic customer | Repeatable conversation and media integration | Human handset audio arrival |
| Real call + acoustic capture | Actual phone experience | Sustained p95 without enough valid samples |

Talker is served on the Mac and tested through Desk. Its health endpoint and playground integration are present, but there is no established Talker inference path in the inspected phone engine. The local edge gateway is another experimentation surface. The target-speaker verification service is the local model endpoint specifically selected by the phone profile.
''',
}

STANDALONE_CSS = '''
:root{color-scheme:light dark;--font-size-base:15px;--background:#f8fafc;--foreground:#182333;--muted-foreground:#526071;--border:#cbd5e1;--primary:#182333;--primary-foreground:#fff;--accent:#e5edf4;--ring:#0f766e;--viz-series-1:#0f766e;--card:#fff}
@media(prefers-color-scheme:dark){:root{--background:#08111d;--foreground:#e4ecf4;--muted-foreground:#a4b5c8;--border:#33475c;--primary:#e4ecf4;--primary-foreground:#08111d;--accent:#223649;--ring:#5eead4;--viz-series-1:#5eead4;--card:#102033}}
*{box-sizing:border-box}body{margin:0;background:var(--background);color:var(--foreground);font:15px/1.5 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{max-width:1100px;margin:auto;padding:36px 24px 64px}h2,h3,p{margin:0}h2{font-size:25px;font-weight:500;letter-spacing:-.03em}h3{font-size:16px;font-weight:500}a{color:var(--viz-series-1);text-underline-offset:3px}button,select{font:inherit}.text-small{font-size:12px}.viz-controls{display:flex;flex-wrap:wrap;align-items:end;gap:16px}.form-label{display:grid;gap:6px;flex:1;min-width:180px}.form-select{width:100%;min-height:42px;padding:8px 10px;border:1px solid var(--border);border-radius:7px;background:var(--background);color:var(--foreground)}.btn{padding:12px;border:1px solid var(--border);background:var(--card);color:var(--foreground);border-radius:8px;cursor:pointer;line-height:1.35;min-height:58px}.viz-tile{width:100%;height:100%}.btn:hover{background:var(--accent)}.btn[aria-pressed=true]{background:var(--primary);color:var(--primary-foreground);border-color:var(--primary)}:focus-visible{outline:3px solid var(--ring);outline-offset:3px}.viz-badge{font-size:12px;padding:3px 8px;background:var(--accent);border-radius:5px}.cursor-interaction{cursor:pointer}body #golive-architecture .ga-standalone-link{display:inline}.guidebar{display:flex;gap:18px;flex-wrap:wrap;margin-bottom:24px;font-size:13px}@media(max-width:560px){main{padding:24px 16px 48px}h2{font-size:22px}}
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fragment', type=Path, help='Also write an inline explorer fragment here')
    args = parser.parse_args()
    data = json.loads((HERE / 'components.json').read_text())
    views = json.loads((HERE / 'views.json').read_text())
    components = data['components']
    ids = [c['id'] for c in components]
    assert len(ids) == len(set(ids)), 'duplicate component id'
    seen = set()
    for view in views:
        assert view['focus'] in view['nodes']
        assert len(view['nodes']) == len(set(view['nodes']))
        seen.update(view['nodes'])
        for a, b, label, kind in view['edges']:
            assert a in view['nodes'] and b in view['nodes'] and label
            assert kind in ('data', 'both', 'control')
    assert seen == set(ids), f'unmapped components: {set(ids) - seen}'
    sources = {p for c in components for p in c['sources']}
    assert all((ROOT / p).exists() for p in sources), 'missing source reference'
    data['directorySources'] = sorted(p for p in sources if (ROOT / p).is_dir())
    for group_id, group in data['groups'].items():
        rows = [c for c in components if c['group'] == group_id]
        body = [f"# {group['title']}", '', '[Architecture index](README.md) · [Interactive explorer](explorer.html)', '',
                f"Source snapshot: `{data['branch']}` at `{data['commit'][:7]}` · {data['snapshot']}.", '', group['intro'], '',
                '```mermaid', DIAGRAMS[group_id], '```', '', EXTRAS[group_id], '', '## Components', '',
                '| Component | Location | Responsibility |', '| --- | --- | --- |']
        body.extend(f"| [{c['name']}](#{c['id']}) | {c['where']} | {c['role']} |" for c in rows)
        for c in rows:
            body.extend(['', f'<a id="{c["id"]}"></a>', '', f"## {c['name']}", '', c['role'], '',
                f"**Boundary:** {c['boundary']}. **Location:** {c['where']}.", '',
                f"**Receives:** {c['input']}", '', f"**Produces:** {c['output']}", '',
                f"**Operation:** {c['operation']}", '', f"**Boundary and limitations:** {c['limit']}", '',
                '**Implementation and detailed runbooks:** ' + ', '.join(f'[{p}](../../{p})' for p in c['sources']) + '.'])
            if c.get('local_source'):
                body.extend(['', f"**Separate local source:** `{c['local_source']}`; read for this architecture, not part of the Git repository."])
        (HERE / group['file']).write_text('\n'.join(body) + '\n')
    template = (HERE / 'explorer.template.html').read_text()
    fragment = template.replace('/*__ARCHITECTURE_DATA__*/', json.dumps(data, ensure_ascii=False).replace('</', '<\\/'))
    fragment = fragment.replace('/*__ARCHITECTURE_VIEWS__*/', json.dumps(views, ensure_ascii=False).replace('</', '<\\/'))
    assert '/*__ARCHITECTURE_' not in fragment
    standalone = '<!doctype html>\n<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>golive architecture</title><style>' + STANDALONE_CSS + '</style></head><body><main><nav class="guidebar"><a href="README.md">Documentation index</a><span>Offline explorer · no API calls or credentials</span></nav>' + fragment + '</main></body></html>\n'
    (HERE / 'explorer.html').write_text(standalone)
    if args.fragment:
        args.fragment.parent.mkdir(parents=True, exist_ok=True)
        args.fragment.write_text(fragment)
    print(f"Generated {len(data['groups'])} guides, {len(views)} views, {len(components)} component descriptions and the standalone explorer.")


if __name__ == '__main__':
    main()
