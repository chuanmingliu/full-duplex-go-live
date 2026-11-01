#timeout 180
#push verify-ready-ack-151b2e3.py /tmp/verify-ready-ack-151b2e3.py
#push ready-ack-call-latency.py /tmp/ready-ack-call-latency.py
set -eu
python3 - <<'PYVERIFY'
from pathlib import Path
import hashlib
p=Path('/tmp/verify-ready-ack-151b2e3.py')
assert hashlib.sha256(p.read_bytes()).hexdigest() == '024e6ff71d6eb9ac402ee74658ba779334a7b200e533d625c384c115c968bf73'
p=Path('/tmp/ready-ack-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest() == '41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850'
p=Path('/tmp/verify-ready-ack-151b2e3.py')
exec(compile(p.read_bytes(),str(p),'exec'))
PYVERIFY
