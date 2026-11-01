#timeout 1200
#push golive-input-timing-302267b-linux /tmp/golive-input-timing-302267b-linux
#push golive-crm-3201ec4-linux /tmp/golive-crm-3201ec4-linux
#push conversation-ab-overrides.json /tmp/conversation-ab-overrides.json
#push run-conversation-ab-20261101.py /tmp/run-conversation-ab-20261101.py
#push verify-conversation-ab-20261101.py /tmp/verify-conversation-ab-20261101.py
#push conversation-ab-call-latency.py /tmp/conversation-ab-call-latency.py
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
p=Path('/tmp/conversation-ab-overrides.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='4dd44f4f6c4d613c64bb2cddbce02e9563b82cd513cc92a305465ef66154b2d8'
p=Path('/tmp/run-conversation-ab-20261101.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='f698d270d9564790c7a75765b61dfbe64d5d835462648c17b3bb24fb4c8da565'
p=Path('/tmp/verify-conversation-ab-20261101.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='91467b8d1ac60e78d1622cce07e327b0331156620bf2307e31fb183e3c6f59a0'
p=Path('/tmp/conversation-ab-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
subprocess.run([sys.executable,'/tmp/run-conversation-ab-20261101.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-conversation-ab-20261101.py'],check=True)
PYJOB
