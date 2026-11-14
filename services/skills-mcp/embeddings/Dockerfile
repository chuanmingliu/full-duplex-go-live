FROM golive-embeddings:pascal-20261114
RUN python3 -m pip uninstall -y grpcio-reflection
RUN python3 - <<'PY'
from pathlib import Path
p=Path('/usr/local/lib/python3.10/dist-packages/vllm/platforms/__init__.py')
s=p.read_text()
assert s.count('version("vllm")') == 1
p.write_text(s.replace('version("vllm")','version("vllm-pascal")'))
PY
