# Original Active Call review, 2026-11-02

Historical runner operations for missed call `out-mvykct0y-gklg` and the
owner-authorized retry `out-mvykfqfn-wibn`. Do not replay: the jobs bind unique
receipt names, a five-minute owner-request window, the previous call and the
verified original runtime/configuration hashes. The retry explicitly verifies
that the missed call has ended before dialing once. Uncertain dial results
must never trigger an automatic redial.

`analyze.py` verifies all five archive files, both audio directions and both
transcript speakers after the owner says done. It exports numeric latency,
interruption and tool evidence only. Private logs stay on the box. The job
requires hash-pinned staged copies of `scripts/call_latency.py` and the
2026-11-01 interruption analyzer; their original staging names are retained.
`details.py` reads separate CRM phase times and numeric interruption traces.

`publish.job` exclusively creates this call's Journal retrospective and
verifies that the Journal API returns it. It does not overwrite another note,
modify issue state, deploy software, alter configuration or place a call.
`journal-retrospective.md` is the exact published payload with GitHub links.
The manifest records executed hashes. Private prompts, recipients, credentials,
transcripts and recordings are excluded from this directory.
