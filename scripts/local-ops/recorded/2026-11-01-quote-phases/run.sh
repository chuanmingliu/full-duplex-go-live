#timeout 600
#push golive-ready-ack-f8e4d83-linux /tmp/golive-ready-ack-f8e4d83-linux
#push golive-crm-3201ec4-linux /tmp/golive-crm-3201ec4-linux
#push run-quote-phases-3201ec4.py /tmp/run-quote-phases-3201ec4.py
#push verify-quote-phases-3201ec4.py /tmp/verify-quote-phases-3201ec4.py
#push ready-ack-call-latency.py /tmp/ready-ack-call-latency.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
p=Path('/tmp/golive-ready-ack-f8e4d83-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='26fb388e620984e8ed82e9b2b55cbbfe28d5dc79843187d95acece0e7e5dd6a3'
p.chmod(0o700)
p=Path('/tmp/golive-crm-3201ec4-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='76f53c252240c94b9947094fbfe7f6533f46acc1da8edee6f3cc8d7023f8bd83'
p.chmod(0o700)
p=Path('/tmp/run-quote-phases-3201ec4.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='8aab4ce90c8adc3991e413929c2a7d9f359497ef871ad478913e332045e34bec'
p=Path('/tmp/verify-quote-phases-3201ec4.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='6c2235c6eb698b02c68f9e0d8985c4014819521fecd3085d21359b49026bf31c'
p=Path('/tmp/ready-ack-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850'
subprocess.run([sys.executable,'/tmp/run-quote-phases-3201ec4.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-quote-phases-3201ec4.py'],check=True)
PYJOB
