#timeout 180
#push verify-silent-ack-f8e4d83.py /tmp/verify-silent-ack-f8e4d83.py
#push ready-ack-call-latency.py /tmp/ready-ack-call-latency.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib
p=Path('/tmp/verify-silent-ack-f8e4d83.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='4f48b579433d02e93ea454ea51e2249ecdcdf64312c947b72a718e2c59d2bbf3'
p=Path('/tmp/ready-ack-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850'
p=Path('/tmp/verify-silent-ack-f8e4d83.py')
exec(compile(p.read_bytes(),str(p),'exec'))
PYJOB
