# Covered-continuation SIP simulator validation

Historical, single-use operation for engine `f12b6f5` and diagnostic CRM
`3201ec4`. Do not replay this job: its unique attempt and receiver IDs are part
of the evidence. Use the existing runner and inspect live state before any new
operation.

`run.py` preserves the earlier 90-second quotation scenario, original provider
profile, saved greeting/voice/instructions, and deliberately slow quote service.
Only the isolated engine binary changes. It refuses occupied real engines,
active simulator runs, occupied experiment ports, an existing attempt, changed
original config hashes or unexpected Journal retention. The destination is
strictly a reserved `sim-*` SIP URI. Isolated processes are terminated afterward;
Active Call identity/start and original file hashes must remain unchanged.

`verify.py` checks the finished session, transcript speakers, both WAV channels,
and every archived file hash. It evaluates conversational response latency with
`--quote-sla separate --target-ms 1150 --min-turns 20 --metric both`, retaining
quotation distributions and evidence-integrity failures. It also counts the new
retired-continuation event and prior acknowledgment-release events. Numeric
results are appended to the authorized Desk work log. No physical handset
measurement or real-call promotion is implied.

`run.sh` is the exact reviewed runner body. `manifest.json` binds its five
payloads. The runner job SHA-256 is
`b53891773a71208d1b2157b8e0d0d4734b3e6fd8ca421c1c838fbb6b284a657a`.
Audio, customer data, credentials and private configuration remain on the box.
