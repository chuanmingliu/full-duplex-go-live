# Quotation phase diagnostic operation

Historical simulator-only runner job using engine f8e4d83 and CRM 3201ec4.
The existing original configurations, quote-service delay and real services are
preserved. Only the CRM diagnostic logging differs from the previous trial.
The job runs one 90-second simulator call, then verifies its five archived
files, both sides of audio/transcript, and the strict 1150 ms / 20-response gate.
It appends the authorized Desk work log. The gate failed; no handset claim.

The fixed attempt directory and call reservation are not reusable. Do not
replay the job or remove its guards. Raw conversations, credentials and quote
payloads remain private on the box. The completed runner receipt has exit 0.
See docs/telephony/2026-11-01-quotation-phase-timing.md for the findings.
