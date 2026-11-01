#timeout 150
#push focus-window-replay-20261101.py /tmp/focus-window-replay-20261101.py
#pull /tmp/focus-window-replay-20261101.json focus-window-replay-20261101.json
set -eu
python3 - <<'PY'
from pathlib import Path
import hashlib,subprocess
p=Path('/tmp/focus-window-replay-20261101.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c398c4e47fe501a28da6cbb720b51a930dbd7911ff0d13ada148661d4f911d3f'
assert not Path('/tmp/focus-window-replay-20261101.json').exists(),'completed probe exists; do not replay'
subprocess.run(['nice','-n','10','/home/chuanming/edge/tsv/.venv/bin/python',str(p)],check=True,timeout=130)
PY
