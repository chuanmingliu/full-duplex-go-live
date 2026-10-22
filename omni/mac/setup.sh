#!/bin/bash
# One-time environment for the omni pipeline on the Mac (run as a mac-runner
# job). Makes $OMNI/.venv with MLX, mlx-vlm (the Qwen3-Omni thinker loader),
# mlx-audio, and the data/eval tools. Prints versions; prints no paths of
# keys.
set -euo pipefail
cd "$OMNI"
echo "== machine"
sysctl -n machdep.cpu.brand_string; echo "$(($(sysctl -n hw.memsize)/1073741824)) GB unified"
sw_vers -productVersion
echo "== python"
PY=""
for c in python3.12 python3.11 python3.13 python3; do
  if command -v "$c" >/dev/null && "$c" -c 'import sys; assert (3,11) <= sys.version_info < (3,14)' 2>/dev/null; then PY="$c"; break; fi
done
[ -n "$PY" ] || { echo "no python 3.11–3.13 on PATH (brew install python@3.12)"; exit 2; }
"$PY" --version
if [ ! -x .venv/bin/python ]; then
  if command -v uv >/dev/null; then uv venv -p "$PY" .venv; else "$PY" -m venv .venv; fi
fi
. .venv/bin/activate
python -m pip install -q --upgrade pip
python -m pip install -q "mlx>=0.31" mlx-vlm mlx-audio "transformers>=4.57" safetensors numpy soundfile jiwer huggingface_hub modelscope pytest pyyaml
echo "== versions"
python - <<'EOF'
import mlx.core as mx, importlib
print("mlx", mx.__version__, "metal", mx.metal.is_available())
for m in ("mlx_vlm", "mlx_audio", "transformers", "huggingface_hub", "modelscope"):
    try: print(m, importlib.import_module(m).__version__)
    except Exception as e: print(m, "ERR", e)
info = mx.metal.device_info() if mx.metal.is_available() else {}
print("device", info.get("device_name"), "max working set GB", round(info.get("max_recommended_working_set_size", 0)/2**30, 1))
EOF
mkdir -p hf ms data checkpoints logs
echo "setup done"
