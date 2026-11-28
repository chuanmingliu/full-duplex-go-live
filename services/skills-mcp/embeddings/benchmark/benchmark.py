#!/usr/bin/env python3
"""Compare embedding retrieval quality and warm request latency on fixed cases."""
import argparse
import hashlib
import platform
from datetime import datetime, timezone
import json
import math
import random
import statistics
import time
from pathlib import Path
from urllib.request import Request, urlopen

import numpy as np

QWEN_INSTRUCTION = (
    'Instruct: Given the current customer request, retrieve the skill description '
    'that best matches the action the assistant should perform.\nQuery: '
)


def percentile(values, fraction):
    return sorted(values)[math.ceil(len(values) * fraction) - 1]


def run(args):
    cases = json.loads(Path(args.cases).read_text())
    skills = cases['skills']
    queries = cases['queries']
    names = [skill['name'] for skill in skills]
    runtime = {'backend': args.backend, 'name': args.name, 'revision': args.revision,
               'platform': platform.platform()}
    synchronize = lambda: None
    load_start = time.perf_counter()

    if args.backend == 'sentence-transformers':
        import torch
        import transformers
        import sentence_transformers
        from sentence_transformers import SentenceTransformer

        torch.set_num_threads(2)
        torch.set_num_interop_threads(2)
        if args.device == 'cuda':
            if not torch.cuda.is_available():
                raise RuntimeError('CUDA execution requested but unavailable')
            synchronize = torch.cuda.synchronize
            torch.cuda.reset_peak_memory_stats()
            runtime.update(gpu=torch.cuda.get_device_name(),
                           capability=list(torch.cuda.get_device_capability()),
                           cuda=torch.version.cuda)
        runtime.update(torch=torch.__version__, transformers=transformers.__version__,
                       sentence_transformers=sentence_transformers.__version__,
                       device=args.device, dtype='float32', threads=2)
        model = SentenceTransformer(args.model_dir, device=args.device,
            local_files_only=True,
            model_kwargs={'torch_dtype': torch.float32, 'attn_implementation': 'sdpa'})
        model.max_seq_length = 512
        runtime['prompts'] = model.prompts
        runtime['query_override'] = QWEN_INSTRUCTION if args.name == 'qwen3' else None

        def embed(texts, query):
            method = model.encode_query if query else model.encode_document
            options = dict(batch_size=32, normalize_embeddings=True,
                           convert_to_numpy=True, show_progress_bar=False)
            if query and args.name == 'qwen3':
                options['prompt'] = QWEN_INSTRUCTION
            return method(texts, **options)
    else:
        runtime.update(url=args.url, model=args.served_model,
                       query_prefix=args.query_prefix, document_prefix=args.document_prefix)

        def embed(texts, query):
            prefix = args.query_prefix if query else args.document_prefix
            body = json.dumps({'model': args.served_model,
                               'input': [prefix + text for text in texts],
                               'encoding_format': 'float'}).encode()
            request = Request(args.url, data=body, headers={'Content-Type': 'application/json'})
            with urlopen(request, timeout=60) as response:
                output = json.load(response)
            rows = sorted(output['data'], key=lambda row: row['index'])
            if [row['index'] for row in rows] != list(range(len(texts))):
                raise ValueError('Unexpected embedding result indices')
            return np.asarray([row['embedding'] for row in rows], dtype=np.float32)

    synchronize()
    load_seconds = time.perf_counter() - load_start
    start = time.perf_counter()
    documents = embed([skill['description'] for skill in skills], False)
    synchronize()
    document_batch_ms = 1000 * (time.perf_counter() - start)
    if not np.isfinite(documents).all():
        raise ValueError('Nonfinite document embeddings')
    if not np.allclose(np.linalg.norm(documents, axis=1), 1, atol=.005):
        raise ValueError('Document embeddings are not normalized')

    for query in queries[:6]:
        embed([query['context']], True)
    synchronize()

    checks = []
    latencies = []
    order = list(range(len(queries)))
    for repetition in range(args.rounds):
        random.Random(100 + repetition).shuffle(order)
        for index in order:
            query = queries[index]
            synchronize()
            start = time.perf_counter()
            vector = embed([query['context']], True)[0]
            synchronize()
            latencies.append(1000 * (time.perf_counter() - start))
            if not np.isfinite(vector).all() or not np.isclose(np.linalg.norm(vector), 1, atol=.005):
                raise ValueError('Invalid query embedding')
            if repetition == 0:
                scores = documents @ vector
                ranking = np.argsort(-scores).tolist()
                rank = ranking.index(names.index(query['skill'])) + 1
                checks.append(dict(index=index, language=query['language'],
                    context=query['context'], expected=query['skill'],
                    actual=names[ranking[0]], expected_rank=rank,
                    top3=[names[i] for i in ranking[:3]],
                    similarity=round(float(scores[ranking[0]]), 5)))

    checks.sort(key=lambda check: check['index'])
    quality = {
        'queries': len(checks),
        'top1_correct': sum(check['expected_rank'] == 1 for check in checks),
        'top3_correct': sum(check['expected_rank'] <= 3 for check in checks),
        'top5_correct': sum(check['expected_rank'] <= 5 for check in checks),
        'mrr': round(statistics.mean(1 / check['expected_rank'] for check in checks), 5),
    }
    for language in ['en', 'zh']:
        selected = [check for check in checks if check['language'] == language]
        quality[language] = dict(total=len(selected),
            top1_correct=sum(check['expected_rank'] == 1 for check in selected),
            top3_correct=sum(check['expected_rank'] <= 3 for check in selected))
    report = dict(runtime=runtime, skills=len(skills), dimensions=documents.shape[1],
        dataset_sha256=hashlib.sha256(Path(args.cases).read_bytes()).hexdigest(),
        timestamp_utc=datetime.now(timezone.utc).isoformat(),
        load_seconds=round(load_seconds, 3), document_batch_ms=round(document_batch_ms, 2),
        warmup_requests=6, quality=quality,
        latency=dict(requests=len(latencies), concurrency=1,
            p50_ms=round(statistics.median(latencies), 2),
            p95_ms=round(percentile(latencies, .95), 2),
            p99_ms=round(percentile(latencies, .99), 2),
            max_ms=round(max(latencies), 2)), cases=checks)
    if args.backend == 'sentence-transformers' and args.device == 'cuda':
        report['peak_cuda_allocated_mib'] = round(torch.cuda.max_memory_allocated() / 2**20, 1)
    Path(args.output).write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    print(json.dumps({key: value for key, value in report.items() if key != 'cases'},
                     ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--name', required=True, choices=['qwen3', 'bge-m3', 'gemma', 'minilm'])
    parser.add_argument('--backend', choices=['sentence-transformers', 'http'], default='sentence-transformers')
    parser.add_argument('--model-dir')
    parser.add_argument('--revision', default='')
    parser.add_argument('--device', choices=['cpu', 'cuda'], default='cuda')
    parser.add_argument('--url')
    parser.add_argument('--served-model')
    parser.add_argument('--query-prefix', default='')
    parser.add_argument('--document-prefix', default='')
    parser.add_argument('--cases', default=str(Path(__file__).with_name('cases.json')))
    parser.add_argument('--rounds', type=int, default=3)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    if args.rounds < 1:
        parser.error('--rounds must be positive')
    if args.backend == 'http' and not (args.url and args.served_model):
        parser.error('HTTP backend requires --url and --served-model')
    if args.backend == 'sentence-transformers' and not args.model_dir:
        parser.error('Sentence Transformers backend requires --model-dir')
    run(args)
