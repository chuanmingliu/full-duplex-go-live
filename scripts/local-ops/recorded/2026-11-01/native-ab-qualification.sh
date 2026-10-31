#timeout 400
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-ab-20261031/run-v7.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='6aa99d9d6e89f19b28dd02c2868fb0fcbddb6979f3d3e8a12e1c81f67dfbd515'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
