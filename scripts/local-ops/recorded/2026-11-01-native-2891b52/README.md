# Native SIP engine 2891b52 deployment

Historical idle-only deployment through the existing Mac runner at 15:58:40,
November 1, 2026. Never replay this consumed release attempt. Exact-source full
CI 41286136566 passed verify and simulate. Focused ownership regressions pass,
including 30 repeated replacement/ack cases. A fresh SIP simulator trial has
10 normal outputs, no missing receipts, two completed scripted interruption
actions, no receiver warnings and verified chat/stereo archives.

This promotes an accounting repair, not a latency-qualified result. Its short
simulation cannot certify the physical handset goal; the completed real call
remains above target and its acoustic capture has invalid clock continuity.

Native engine 9895 is ready/idle/capacity one and reports 2891b52. No-dial adapter
preflight passed. Original configuration hashes, native Journal and Active Call
identity/start are unchanged; original engines on 8765/9875/9885 are preserved.
Adapter remains fb11e2c. The preceding 7fc4237 binary is retained at the backup
path in receipt.json. Idle guards, binary/config hashes and a deployment lock
protect the switch; the script restores that backup if verification fails and
no new call is active. No real call was placed by this operation.
