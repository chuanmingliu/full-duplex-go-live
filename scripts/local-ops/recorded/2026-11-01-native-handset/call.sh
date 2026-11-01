#timeout 100
#push call-native-handset-20261101.py /tmp/call-native-handset-20261101.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
p=Path('/tmp/call-native-handset-20261101.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='defe3a5af86b2be27f45a6ef839e43d8fc92ba0a61064a27860f09310d6fb702'
subprocess.run([sys.executable,str(p)],check=True)
PYJOB
