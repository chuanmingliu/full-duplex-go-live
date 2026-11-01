#timeout 90
#push collect-input-timing-302267b.py /tmp/collect-input-timing-302267b.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/collect-input-timing-302267b.py').read_bytes()).hexdigest()=='3b26dcaab5220209993b0295cb3c6f913075cd93131d6f64a5b37aa343042dbc'
assert hashlib.sha256(Path('/tmp/input-timing-call-latency.py').read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
subprocess.run([sys.executable,'/tmp/collect-input-timing-302267b.py'],check=True)
PYJOB
