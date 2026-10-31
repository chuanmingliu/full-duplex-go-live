#timeout 750
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-ab-20261031/run-v2.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='d37f24e3ed9ab24bbd3db101705a97d892bf3acd4d29f9e263306a9ddd103e11'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
