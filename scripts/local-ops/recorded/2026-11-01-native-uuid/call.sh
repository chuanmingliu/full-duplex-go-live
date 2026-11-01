#timeout 100
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c/call-a93db0f8-ff19-4b9d-95f3-09aca39541be.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='7b9e6f24dbd1ae2419e07a06dc9c041336e9025000fdec3bddafe9785acd4242'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
