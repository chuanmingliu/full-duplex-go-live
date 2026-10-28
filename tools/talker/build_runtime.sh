#!/usr/bin/env bash
# Build the pinned, CPU-only llama.cpp runtime outside the source repository.
set -euo pipefail
umask 077

if [[ ${1:-} == --help ]]; then
  cat <<'EOF'
Usage: tools/talker/build_runtime.sh

Requires git, CMake >= 3.14, a C++17 compiler, and make (or CMake's default generator).
Downloads public llama.cpp source; never reads call data or starts a service.

Environment overrides:
  TALKER_HOME       Artifact root (default: $HOME/golive-talker)
  LLAMA_CPP_SOURCE  Source tree (default: $TALKER_HOME/llama.cpp)
  CMAKE            CMake executable (default: cmake)
  BUILD_JOBS       Parallel build jobs (default: 3)

Example on the benchmark box:
  CMAKE="$HOME/edge/hfcli/bin/cmake" tools/talker/build_runtime.sh

Outputs: $LLAMA_CPP_SOURCE/build/bin/llama-server and llama-quantize.
Run the server with --n-gpu-layers 0 --host 127.0.0.1 on a dedicated port.
EOF
  exit 0
fi
if [[ $# -ne 0 ]]; then
  echo 'Unexpected argument; use --help.' >&2
  exit 2
fi

revision=15fa3c493bfcd040b5f4dcb29e1c998a0846de16
tag=b8920
runtime_root=${TALKER_HOME:-$HOME/golive-talker}
source_dir=${LLAMA_CPP_SOURCE:-$runtime_root/llama.cpp}
cmake_bin=${CMAKE:-cmake}
build_jobs=${BUILD_JOBS:-3}
[[ $build_jobs =~ ^[1-9][0-9]*$ ]] || { echo 'BUILD_JOBS must be a positive integer.' >&2; exit 2; }
command -v git >/dev/null
command -v "$cmake_bin" >/dev/null
mkdir -p "$runtime_root/runtime" "$runtime_root/models" "$runtime_root/results"

if [[ ! -d $source_dir/.git ]]; then
  if [[ -e $source_dir ]]; then
    echo 'LLAMA_CPP_SOURCE already exists and is not a Git checkout.' >&2
    exit 2
  fi
  git clone --depth 1 --branch "$tag" https://github.com/ggml-org/llama.cpp "$source_dir"
fi
actual_revision=$(git -C "$source_dir" rev-parse HEAD)
if [[ $actual_revision != "$revision" ]]; then
  printf 'Wrong llama.cpp revision: expected %s; found %s. Use a separate checkout.\n' "$revision" "$actual_revision" >&2
  exit 2
fi
if [[ -n $(git -C "$source_dir" status --porcelain --untracked-files=no) ]]; then
  echo 'llama.cpp has tracked modifications; use an unmodified pinned checkout.' >&2
  exit 2
fi

"$cmake_bin" -S "$source_dir" -B "$source_dir/build" \
  -DCMAKE_BUILD_TYPE=Release \
  -DGGML_NATIVE=ON -DGGML_CPU=ON -DGGML_BLAS=OFF \
  -DGGML_CUDA=OFF -DGGML_VULKAN=OFF -DGGML_METAL=OFF \
  -DGGML_SYCL=OFF -DGGML_OPENCL=OFF \
  -DLLAMA_BUILD_TESTS=OFF \
  > "$runtime_root/runtime/build.log" 2>&1
"$cmake_bin" --build "$source_dir/build" \
  --target llama-server llama-quantize --parallel "$build_jobs" \
  >> "$runtime_root/runtime/build.log" 2>&1

"$source_dir/build/bin/llama-server" --version
printf 'revision=%s\nserver=%s\nquantizer=%s\nbuild_log=%s\n' \
  "$actual_revision" "$source_dir/build/bin/llama-server" \
  "$source_dir/build/bin/llama-quantize" "$runtime_root/runtime/build.log"
