#timeout 600
#push golive-covered-f12b6f5-linux /tmp/golive-covered-f12b6f5-linux
#push golive-crm-3201ec4-linux /tmp/golive-crm-3201ec4-linux
#push run-covered-ack-f12b6f5.py /tmp/run-covered-ack-f12b6f5.py
#push verify-covered-ack-f12b6f5.py /tmp/verify-covered-ack-f12b6f5.py
#push covered-ack-call-latency.py /tmp/covered-ack-call-latency.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
p=Path('/tmp/golive-covered-f12b6f5-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='7d367f327be47ee6abfd29e0bca5bc9cecc940ebacf0130c3bda3cab2bc166a4'
p.chmod(0o700)
p=Path('/tmp/golive-crm-3201ec4-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='76f53c252240c94b9947094fbfe7f6533f46acc1da8edee6f3cc8d7023f8bd83'
p.chmod(0o700)
p=Path('/tmp/run-covered-ack-f12b6f5.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='3c5ca1649b86f39c78ae0b101a383047bb9d44dff37912184ca8bcbcf9913538'
p=Path('/tmp/verify-covered-ack-f12b6f5.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='8c01036d06bfe01b54b863aec6dc259eb29bd8538ee8dbae58f46e28933e059e'
p=Path('/tmp/covered-ack-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='793ae0b99e3ae4b3bf68164ea9c4badd3717d624d6f630c3e35c91f6e67ab966'
subprocess.run([sys.executable,'/tmp/run-covered-ack-f12b6f5.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-covered-ack-f12b6f5.py'],check=True)
PYJOB
