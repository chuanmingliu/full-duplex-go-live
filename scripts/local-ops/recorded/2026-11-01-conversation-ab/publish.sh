#timeout 60
#push conversation-ab-notes-20261101.json /tmp/conversation-ab-notes-20261101.json
#push publish-conversation-ab-20261101.py /tmp/publish-conversation-ab-20261101.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/conversation-ab-notes-20261101.json').read_bytes()).hexdigest()=='c48079995d6dfbf5500bc0bcd1e7385928e980399d5f67ab198d64673a05819c'
assert hashlib.sha256(Path('/tmp/publish-conversation-ab-20261101.py').read_bytes()).hexdigest()=='8f48da65db1d14a06f379214aa949788f7dea528ae61f9c81fca5024c7edc8ca'
subprocess.run([sys.executable,'/tmp/publish-conversation-ab-20261101.py'],check=True)
PYJOB
