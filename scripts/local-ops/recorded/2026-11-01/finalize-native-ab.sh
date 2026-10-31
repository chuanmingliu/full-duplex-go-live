#timeout 100
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-ab-20261031/finalize-native-ab.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='fcaed7c70bec64a630606857fc9f87ab9c55f4dbca0e6e720cd2f30335419be9'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
