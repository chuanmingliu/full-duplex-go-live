# Pascal GPU skill embeddings

BGE-M3 and Qwen3-Embedding-0.6B are deployed on the ThinkPad P71 Quadro P5000 through the community Pascal build of vLLM 0.8.5.post1. BGE-M3 is the primary member of the ordered embedding pool. The endpoint is `http://127.0.0.1:8796/v1/embeddings`, with model name `bge-m3` and 1024-dimensional normalized vectors.

## Model selection

The development fixture contains 24 related skill descriptions and 96 labelled queries, split equally between English and Chinese. It includes closely related actions such as reporting a claim and checking its status. Each model received its documented query/document formatting. No model was fine-tuned on this fixture.

| Model | Correct top-1 / 96 | Correct within top-3 / 96 | GPU p50 | GPU p95 | Peak allocated CUDA memory |
| --- | --- | --- | --- | --- | --- |
| BGE-M3 | 83 | 96 | 21.17 ms | 24.23 ms | 2200 MiB |
| Qwen3-Embedding-0.6B | 84 | 96 | 32.94 ms | 40.98 ms | 2405 MiB |
| EmbeddingGemma-300M | 91 | 95 | 37.08 ms | 46.67 ms | 1237 MiB |

These three rows use the same Sentence Transformers 5.1.1 backend, Transformers 4.57.6, PyTorch 2.6.0+cu124, FP32, SDPA, and the actual P5000. Each latency measurement includes tokenization and one query embedding, with CUDA synchronization. There are 288 timed requests per model, six excluded warmups, concurrency one, and a maximum sequence length of 512 tokens. Description embeddings were prepared separately. Raw cases and results are under `benchmark/`.

BGE-M3 was selected for shortlisting because it had the lowest latency and retrieved the expected skill within its first three results for every fixture query. The host should still inspect the retrieved skills before selection. EmbeddingGemma had the strongest top-1 result. The Pascal vLLM version used here lacks its native bidirectional embedding and projection support.

This is a synthetic development benchmark. Production routing accuracy and large-catalog retrieval quality require their own evaluation.

## vLLM verification

BGE-M3 through the deployed vLLM HTTP endpoint measured **22.51 ms p50 and 24.55 ms p95**, with the same 83/96 top-1 and 96/96 top-3 results.

The serving configuration uses V0, XFormers, eager execution, FP32 and CLS pooling with normalization. Fourteen mixed-language inputs matched the reference embeddings with maximum absolute error `1.90e-7`. Batched and individual API results differed by at most `1.35e-7`.

The real endpoint passed all eight English/Chinese MCP integration searches. The QuickJS flow called `skills_search`, `skills_resolve`, and `skills_load` successfully. The integration test passed again after deployment restart. The MCP fixture uses synthetic data and enables the selection companion explicitly. Production MCP cutover remains pending.

The MCP service also passed `go test ./...`, `go test -race ./...`, and `go vet ./...`. Its default selection policy enables the companion above 50 available skills. Always-on activation still requires end-to-end evaluation.

## Deployment

The deployed directory is `/home/chuanming/golive-embeddings/releases/bge-pascal-20261114`, also targeted by `/home/chuanming/golive-embeddings/current`.

The model directory is `/home/chuanming/golive-embeddings/models/bge-m3-5617a9f`. Its Hugging Face revision is `5617a9f61b028005a4858fdac845db406aefb181`. The model manifest records file checksums.

Run the checked Compose definition from the release directory.

```sh
docker compose up -d --pull never
curl --fail http://127.0.0.1:8796/health
```

The Compose service has an `unless-stopped` restart policy and an HTTP health check. It uses two CPU cores, an 8 GiB host-memory limit, and the existing NVIDIA devices and driver libraries. Its model mount is read-only. The API listens on loopback. The tested NVIDIA driver is 580.178.04, and the GPU compute capability is 6.1.

Configure an MCP process on the ThinkPad host with the following environment.

```sh
SKILLS_EMBEDDING_URL=http://127.0.0.1:8796/v1/embeddings
SKILLS_EMBEDDING_MODEL=bge-m3
SKILLS_EMBEDDING_QUERY_PREFIX=
SKILLS_EMBEDDING_DOCUMENT_PREFIX=
```

The release contains these settings in `skills-mcp.env`. A bridged container needs connectivity to the host endpoint before using that URL.

## Embedding service pool

The selected pair is BGE-M3 as primary and Qwen3-Embedding-0.6B as warm backup.
Both achieved 100% top-3 recall on the 96-query synthetic bilingual fixture.
The actual vLLM HTTP measurements use 288 requests per endpoint at concurrency
one, with both services resident on the same GPU during the Qwen measurement.
The BGE measurement preceded the Qwen deployment.

| Endpoint | Model | Top-1 / 96 | Top-3 / 96 | HTTP p50 | HTTP p95 |
| --- | --- | --- | --- | --- | --- |
| `127.0.0.1:8796` | `bge-m3` | 83 | 96 | 22.51 ms | 24.55 ms |
| `127.0.0.1:8797` | `qwen3-embedding-0.6b` | 84 | 96 | 50.38 ms | 61.37 ms |

`pool.json` records the ordered members, pinned revisions and model-specific
query formatting. The deployed copy is
`/home/chuanming/golive-embeddings/embedding-pool.json`. Qwen uses normalized LAST
pooling and its retrieval instruction prefix. Its release directory is
`/home/chuanming/golive-embeddings/releases/qwen-pascal-20261114`, with model files
at `/home/chuanming/golive-embeddings/models/qwen3-embedding-97b0c61`.
`compose.qwen.yaml` reproduces that service using the same runtime image.

Configure a compatible MCP host process with the following setting.

```sh
SKILLS_EMBEDDING_POOL_FILE=/home/chuanming/golive-embeddings/embedding-pool.json
```

This takes precedence over single-provider settings. The BGE release's
`skills-mcp.env` contains this setting alongside the legacy BGE settings.
Each provider has a separate description-vector index and query cache.
Search replies include `embedding_provider` and `embedding_revision`.
Search has a shared 500 ms budget, with up to 250 ms reserved for the first of
two members. Failed members have a five-second circuit cooldown followed by a
single request-driven recovery attempt. If all providers fail, `auto` retrieval
uses the lexical index and explicit `semantic` retrieval returns an error.
Startup/publication requires one complete index and repairs unavailable members
in the background on subsequent searches.

Live tests used the real model endpoints and synthetic MCP skill fixtures.
Local proxies injected HTTP 503 responses while the deployed services stayed
available. BGE-to-Qwen failover returned the expected quotation skill in
50.41 ms in the recorded request. Qwen handled all eight bilingual fixture
queries. BGE recovered after the cooldown, and the reverse Qwen-to-BGE failover
also passed. The separate QuickJS search → resolve → verified load flow passed.
Raw evidence is in `benchmark/results/pool-live.log` and
`benchmark/results/qwen-vllm-http.json`.

Both model services are deployed and healthy. The updated MCP pool implementation
has passed isolated live tests. Production MCP cutover remains pending.
Both members share one host and GPU, so they provide model-service failover
while host or GPU failure affects the entire pool.

To repeat the live tests from `services/skills-mcp` on the ThinkPad host,

```sh
GOLIVE_TEST_EMBEDDINGS=1 \
SKILLS_EMBEDDING_POOL_FILE=/home/chuanming/golive-embeddings/embedding-pool.json \
go test -run '^TestEmbedding(PoolLiveEndpoints|LiveEndpoint)$' -v
```

## Rebuilding

The build reused the existing local `qwen3-asr-ws:0.6b` image to avoid a large remote image pull. Its recorded identity was `sha256:56154afb869d53653165856959673c57b7b6e995a41aa31d5aa358de1e548a71`. This cached base includes unrelated ASR packages and retains a pre-existing PyGObject/pycairo dependency warning in a full `pip check`. The embedding runtime is covered by the functional checks above.

Verified offline wheels already reside in `/home/chuanming/golive-embeddings/pascal-build/wheels`. `wheels.lock.json` records their official download URLs and SHA-256 hashes. The two Dockerfiles reproduce the tested build stages. Copy them into that build directory before running the following commands.

```sh
docker build --network none -f Dockerfile.base -t golive-embeddings:pascal-20261114 .
docker build --network none -f Dockerfile -t golive-embeddings:pascal-20261114-runtime .
```

The runtime patch changes GPU platform discovery to look up the installed distribution name `vllm-pascal`. The serving Compose definition selects XFormers. The CPU-oriented vLLM Torch SDPA metadata builder was incompatible with its GPU runner in this version.

The final tested runtime image identity is `sha256:de2b15617ecebafa1ed137fb6323ecc99bf4e6f7b07977b7408f343511f8c3ea`.

## Repeating the measurements

The benchmark accepts local Sentence Transformers models and OpenAI-compatible HTTP embedding endpoints. The checked JSON files include model revisions, formatting, individual rankings, timing conditions and the fixture SHA-256.

```sh
python benchmark/benchmark.py --name bge-m3 --backend http \
  --url http://127.0.0.1:8796/v1/embeddings --served-model bge-m3 \
  --revision 5617a9f61b028005a4858fdac845db406aefb181 \
  --rounds 3 --output bge-vllm-http.json

GOLIVE_TEST_EMBEDDINGS=1 \
SKILLS_EMBEDDING_URL=http://127.0.0.1:8796/v1/embeddings \
SKILLS_EMBEDDING_MODEL=bge-m3 \
SKILLS_EMBEDDING_QUERY_PREFIX= SKILLS_EMBEDDING_DOCUMENT_PREFIX= \
go test -run '^TestEmbeddingLiveEndpoint$' -v
```

Run the Go command from `services/skills-mcp`. The HTTP benchmark needs NumPy. Local-model measurements additionally need the versions listed above and downloaded model files.

## Sources

- [Pascal-compatible vLLM packages](https://github.com/sasha0552/pascal-pkgs-ci)
- [BGE-M3 model](https://huggingface.co/BAAI/bge-m3)
- [Qwen3 embedding model](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B)
- [EmbeddingGemma model](https://huggingface.co/google/embeddinggemma-300m)
