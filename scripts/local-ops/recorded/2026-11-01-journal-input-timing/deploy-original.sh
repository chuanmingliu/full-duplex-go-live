#timeout 120
#push golive-journal-302267b-linux /tmp/golive-journal-302267b-linux
#push deploy-journal-input-timing.py /tmp/deploy-journal-input-timing.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/golive-journal-302267b-linux').read_bytes()).hexdigest()=='ca3e4d8adfa306c6e3533668f8c6fd993c2826ba93aa7703153f7c81883a02b4'
assert hashlib.sha256(Path('/tmp/deploy-journal-input-timing.py').read_bytes()).hexdigest()=='4b8496b89ef7b916066d5625056d445d26223569d3c19890543c8f49af461d36'
subprocess.run([sys.executable,'/tmp/deploy-journal-input-timing.py'],check=True)
PYJOB
