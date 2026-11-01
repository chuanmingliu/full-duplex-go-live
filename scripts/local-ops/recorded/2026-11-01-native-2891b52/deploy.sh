#timeout 180
#push native-2891b52-ci.json /tmp/native-2891b52-ci.json
#push deploy-native-2891b52.py /tmp/deploy-native-2891b52.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/native-2891b52-ci.json').read_bytes()).hexdigest()=='145faaf952da0f853ee5400da061779834db8fb2459cf6264db3775af68e28a3'
assert hashlib.sha256(Path('/tmp/deploy-native-2891b52.py').read_bytes()).hexdigest()=='abc6245f4af711b9fad852e649f41deebfe22d045086e6f28922ca1ae8286c41'
assert hashlib.sha256(Path('/tmp/golive-receipt-2891b52-linux').read_bytes()).hexdigest()=='82152bb45d6ef50a57370e9dd8c6d26caf60db6c4be566015586dd8dd8fa7472'
subprocess.run([sys.executable,'/tmp/deploy-native-2891b52.py'],check=True)
PYJOB
