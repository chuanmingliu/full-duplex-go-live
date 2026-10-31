#timeout 120
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib,subprocess
r=Path('/home/chuanming/golive-sip-ab/native-ab-20261031')
checks=[('publish-native-ab-notes.py', '215c6a1633938f7213869c448b0b1ab9bb4af113538654d2bcf2c6889a079b49'), ('import-native-ab-journal.py', '143d1ef8a123c00281fe1e694826422961f47ceed7fd69906f638ee978625deb')]
for name,sha in checks:
 p=r/name
 assert hashlib.sha256(p.read_bytes()).hexdigest()==sha
for name,_ in checks:
 subprocess.run(['python3',str(r/name)],check=True)
CHECK
