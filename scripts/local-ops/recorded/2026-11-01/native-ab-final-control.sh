#timeout 400
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-ab-20261031/run-v5.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='5655c21c47b08fe5e61db599cf0ab95da894eeb26620fbd0589c76b4ea8c1501'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
