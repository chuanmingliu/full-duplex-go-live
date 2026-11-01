#timeout 180
#push golive-new-caller-7fc4237-linux /tmp/golive-new-caller-7fc4237-linux
#push native-7fc4237-ci.json /tmp/native-7fc4237-ci.json
#push deploy-native-7fc4237.py /tmp/deploy-native-7fc4237.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/golive-new-caller-7fc4237-linux').read_bytes()).hexdigest()=='7d0d1e61713e85b6f903bfaa2aff46eebd3e834c8c5660b7d4a6bc93396ac65d'
assert hashlib.sha256(Path('/tmp/native-7fc4237-ci.json').read_bytes()).hexdigest()=='65aa06b30019204641d9255eac2f604cad8b3cffaebce17e75fe6f10e017efd6'
assert hashlib.sha256(Path('/tmp/deploy-native-7fc4237.py').read_bytes()).hexdigest()=='6ce0c5bd11b49612ea45dc8cee3da4734ac9950e04499ba43b9b144104fb8f21'
subprocess.run([sys.executable,'/tmp/deploy-native-7fc4237.py'],check=True)
PYJOB
