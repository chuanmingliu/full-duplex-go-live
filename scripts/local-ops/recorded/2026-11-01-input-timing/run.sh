#timeout 600
#push golive-input-timing-302267b-linux /tmp/golive-input-timing-302267b-linux
#push golive-crm-3201ec4-linux /tmp/golive-crm-3201ec4-linux
#push run-input-timing-302267b.py /tmp/run-input-timing-302267b.py
#push verify-input-timing-302267b.py /tmp/verify-input-timing-302267b.py
#push input-timing-call-latency.py /tmp/input-timing-call-latency.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
p=Path('/tmp/golive-input-timing-302267b-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='df8ff30fdc4a40c865806c173d77687caf5bee4833b94b68b67fe529f8b1b31e'
p.chmod(0o700)
p=Path('/tmp/golive-crm-3201ec4-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='76f53c252240c94b9947094fbfe7f6533f46acc1da8edee6f3cc8d7023f8bd83'
p.chmod(0o700)
p=Path('/tmp/run-input-timing-302267b.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='a6f6fe2093c7f731dfbcad01f465c60291c2afdbeecee40b55526868f1f9bcfb'
p=Path('/tmp/verify-input-timing-302267b.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='b1430c51db44252c658e39184aec6bf5c3e015ebd90f17957d416a69bfdeb3b3'
p=Path('/tmp/input-timing-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
subprocess.run([sys.executable,'/tmp/run-input-timing-302267b.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-input-timing-302267b.py'],check=True)
PYJOB
