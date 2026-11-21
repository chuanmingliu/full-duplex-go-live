"""Compare BGE vLLM embeddings with the reference model inside the test container."""
import json
from pathlib import Path
from urllib.request import Request, urlopen

import numpy as np
import torch
from sentence_transformers import SentenceTransformer


def embed(inputs):
    request = Request(
        'http://127.0.0.1:8796/v1/embeddings',
        data=json.dumps({
            'model': 'bge-m3',
            'input': inputs,
            'encoding_format': 'float',
        }).encode(),
        headers={'Content-Type': 'application/json'},
    )
    with urlopen(request, timeout=60) as response:
        body = json.load(response)
    rows = sorted(body['data'], key=lambda row: row['index'])
    assert [row['index'] for row in rows] == list(range(len(inputs)))
    return np.array([row['embedding'] for row in rows], dtype=np.float32)


def main():
    torch.set_num_threads(2)
    model = SentenceTransformer(
        '/model',
        device='cuda',
        local_files_only=True,
        model_kwargs={
            'torch_dtype': torch.float32,
            'attn_implementation': 'sdpa',
        },
    )
    model.max_seq_length = 512
    cases = json.loads(Path('/checks/cases.json').read_text())
    texts = [item['context'] for item in cases['queries'][::7]]
    reference = model.encode(
        texts, normalize_embeddings=True, show_progress_bar=False,
    )
    actual = embed(texts)
    cosines = np.sum(reference * actual, axis=1)
    maximum_error = float(np.max(np.abs(reference - actual)))
    individual_results = np.concatenate([embed([text]) for text in texts])
    batch_error = float(np.max(np.abs(individual_results - actual)))

    assert np.isfinite(actual).all()
    assert np.allclose(np.linalg.norm(actual, axis=1), 1, atol=.001)
    assert np.min(cosines) > .999, (cosines, maximum_error)
    assert maximum_error < .01, maximum_error
    assert batch_error < .001, batch_error

    report = {
        'cases': len(texts),
        'dimensions': actual.shape[1],
        'minimum_reference_cosine': float(np.min(cosines)),
        'maximum_reference_absolute_error': maximum_error,
        'maximum_batch_single_error': batch_error,
    }
    Path('/checks/bge-vllm-parity.json').write_text(json.dumps(report, indent=2))
    print(json.dumps(report), flush=True)


if __name__ == '__main__':
    main()
