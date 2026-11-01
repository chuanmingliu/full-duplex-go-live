#timeout 700
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/tmp/run-tool-ack-focused.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='40b5fc7447a3e67e394f7ab785faa3c176c5df2c50b86a4550c053ba5df241c0'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
