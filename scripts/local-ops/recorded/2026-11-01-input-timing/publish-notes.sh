#timeout 60
#push input-timing-notes-20261101.json /tmp/input-timing-notes-20261101.json
#push publish-input-timing-notes-20261101.py /tmp/publish-input-timing-notes-20261101.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/input-timing-notes-20261101.json').read_bytes()).hexdigest()=='66840b56540a32325910689d663cfa2b9f158f0d46075a8e0961f380f28cf398'
assert hashlib.sha256(Path('/tmp/publish-input-timing-notes-20261101.py').read_bytes()).hexdigest()=='928137eee24c8a88a486c629568a409cc32d55b898f911c5439afc0b452ddd67'
subprocess.run([sys.executable,'/tmp/publish-input-timing-notes-20261101.py'],check=True)
PYJOB
