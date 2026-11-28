#!/usr/bin/env python3
"""Paired real-model comparison through the existing direct and code MCP endpoints."""
import copy
import hashlib
import itertools
import json
import os
from pathlib import Path
import re
import time
import urllib.error
import urllib.request

BASE = os.environ['CODE_MODE_MCP_URL']
MODEL = os.environ['CODE_MODE_REAL_MODEL']
KEY = os.environ['CODE_MODE_LLM_KEY']
OUT = Path(os.environ['GOLIVE_CODE_MODE_DEMO_TRACE'])
MEMORY = {'quote_plan': 'standard', 'quote': {'交强险': True, '车损险': True, '三者险': '200万', '座位险': True},
          'vehiclePrice': 150000, 'vehicleAge': 3, 'ncd': 0, 'seats': 5}
BUDGETS = [4500, 4700, 4900, 5100, 5300, 5500]
DIMENSIONS = {'三者险': ['100万', '200万', '300万', '500万'], '划痕险': [False, '2000', '5000'],
              '驾乘险': [False, '30万', '50万', '80万'], '座位险': [False, True]}
TASK = '''Evaluate every combination in this hypothetical insurance comparison for one customer.
Keep 交强险 and 车损险 enabled. Consider 三者险 at 100万, 200万, 300万, or 500万; 划痕险 disabled, 2000, or 5000; 驾乘险 disabled, 30万, 50万, or 80万; and 座位险 disabled or enabled. These 96 combinations use the active standard demo plan's coverages and tiers. They are valid options for this task. A disabled coverage is represented as false. Do not add other coverages or alter the confirmed selection.
For each budget of 4500, 4700, 4900, 5100, 5300, and 5500 CNY, choose an affordable combination using this strict order of preferences: highest 三者险 limit, then highest 驾乘险 limit, then enabled 座位险, then highest 划痕险 limit, then lowest total premium. Use actual quoted totals for all 96 combinations; prices are opaque and must not be extrapolated.
Return only JSON with evaluated (number of distinct combinations actually priced) and recommendations (six objects in ascending budget order, each containing budget, total, and selection). Each selection must contain all six coverage keys, with false for disabled coverages. This is a hypothetical comparison using synthetic inputs and demo pricing. Neither purchase nor modification of the customer's current plan is requested.'''


def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':'))


def http_json(url, data=None, headers=None):
    body = encoded(data).encode() if data is not None else None
    request = urllib.request.Request(url, data=body, headers=headers or {})
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    return json.loads(raw), (time.perf_counter() - started) * 1000, status


def rpc(mode, method, params):
    params = copy.deepcopy(params)
    params['_meta'] = {'io.modelcontextprotocol/protocolVersion': '2026-07-28',
                       'io.modelcontextprotocol/clientInfo': {'name': 'paired-code-mode-demo', 'version': '1'},
                       'io.modelcontextprotocol/clientCapabilities': {'extensions': {'io.modelcontextprotocol/skills': {}}}}
    request = {'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params}
    headers = {'Authorization': 'Bearer runtime-private', 'Content-Type': 'application/json',
               'Accept': 'application/json, text/event-stream', 'Mcp-Protocol-Version': '2026-07-28', 'Mcp-Method': method}
    if 'name' in params or 'uri' in params:
        headers['Mcp-Name'] = params.get('name', params.get('uri'))
    response, elapsed, status = http_json(BASE + ('/mcp/code' if mode == 'code' else '/mcp'), request, headers)
    return response, {'method': method, 'request': request, 'response': response, 'elapsed_ms': elapsed, 'http_status': status}


def metrics():
    return http_json(BASE + '/demo/metrics', headers={'Authorization': 'Bearer runtime-private'})[0] or []


def content(response):
    result = response.get('result', response)
    if result.get('isError') or 'error' in response:
        return result
    # Both modes receive the structured value once, avoiding duplicate MCP text serialization.
    return result.get('structuredContent', result)


def bind(args, call, revision):
    return dict(args, call=call, memory=MEMORY, memory_version=7, **({'catalog_revision': revision} if revision else {}))


def selection_key(selection):
    return tuple(selection.get(k, False) for k in DIMENSIONS)


def preference(selection, total):
    values = [DIMENSIONS[k].index(selection.get(k, False)) for k in ('三者险', '驾乘险', '座位险', '划痕险')]
    return (*values, -total)


def oracle():
    configs = [dict(zip(DIMENSIONS, values)) for values in itertools.product(*DIMENSIONS.values())]
    rows = []
    for start in range(0, len(configs), 16):
        result, _ = rpc('direct', 'tools/call', {'name': 'quote_total', 'arguments': bind({'configs': configs[start:start+16]}, 'oracle', None)})
        quote = content(result)
        assert quote['status'] == 'quote', quote
        rows.extend(quote['variants'])
    assert len(rows) == 96 and all('error' not in row for row in rows)
    expected = []
    for budget in BUDGETS:
        best = max((row for row in rows if row['total'] <= budget), key=lambda row: preference(row['selection'], row['total']))
        selection = {key: best['selection'].get(key, False) for key in ('交强险', '车损险', *DIMENSIONS)}
        expected.append({'budget': budget, 'total': best['total'], 'selection': selection})
    return {'evaluated': 96, 'recommendations': expected}


def prepare(mode):
    events = []
    def get(method, params):
        result, event = rpc(mode, method, params)
        events.append(event)
        return result['result']
    tools = get('tools/list', {})['tools']
    get('skills/list', {})
    routing = get('prompts/get', {'name': 'outbound-turn', 'arguments': {'event': 'quote.compare', 'state': '{}'}})
    def skill(uri):
        manifest = get('skills/get', {'uri': uri})['skill']
        body = get('resources/read', {'uri': uri})['contents'][0]['text']
        resource = manifest['resources'][0]
        assert resource['digest'] == 'sha256:' + hashlib.sha256(body.encode()).hexdigest()
        assert resource['size'] == len(body.encode())
        return body, manifest
    business = '\n'.join(skill(message['content']['uri'])[0] for message in routing['messages'])
    guide, revision = '', None
    if mode == 'code':
        guide, manifest = skill('skill://mcp-code-mode/runtime/SKILL.md')
        revision = manifest['_meta']['ai.liuchuanming.golive/code-mode']['catalog_revision']
        guide = re.sub(r'```[^\n]*\n.*?```', '', guide, flags=re.S)
        guide += '\nDiscovery API signatures: search(query: string); describe(path: string). Both return promises.\n'
    functions = []
    for tool in tools:
        schema = copy.deepcopy(tool['inputSchema'])
        for name in ('call', 'memory', 'memory_version', 'catalog_revision'):
            schema['properties'].pop(name, None)
        if 'required' in schema:
            schema['required'] = [name for name in schema['required'] if name in schema['properties']]
        description = tool['description']
        if mode == 'code':
            description = 'Execute an async JavaScript function body. Supports top-level await and return, search(query), describe(path), and tools.<name>(args). Use the supplied runtime guide.'
        functions.append({'type': 'function', 'function': {'name': tool['name'], 'description': description, 'parameters': schema}})
    system = '''Complete the user's task accurately using the supplied business skill and available tools. Choose an efficient plan. Multiple independent tool calls may be included in one assistant response.
For bulk tasks, batch independent operations and use available computation capabilities for deterministic filtering and ranking. Avoid another model round trip for work that the current tool invocation can perform. Return compact final results from computation instead of intermediate rows. Reuse obtained data rather than querying the same data again.
The host supplies the authoritative call ID, memory, and memory version for every business call. Only supply business arguments. The confirmed memory is given below. The quote_total service accepts at most 16 alternatives per invocation. Use actual tool results and report failures honestly. Final output must match the user's JSON format.
The active quotation plan for this isolated demo is standard. Its actual configured coverages include 交强险, 车损险, 三者险, 座位险, 划痕险, 玻璃险, and 驾乘险. The task specifies the permitted tiers. Rules concerning motor-full apply when that plan is selected.
Verified business guidance
''' + business + '\nRuntime guide\n' + guide + '\nConfirmed memory\n' + encoded(MEMORY)
    return [{'role': 'system', 'content': system}, {'role': 'user', 'content': TASK}], functions, revision, events


def run_trial(mode, repeat, expected):
    backend_start = len(metrics())
    started = time.perf_counter()
    messages, functions, revision, discovery = prepare(mode)
    discovery_ms = (time.perf_counter() - started) * 1000
    run = {'mode': mode, 'repeat': repeat, 'discovery': discovery, 'model': [], 'executions': [],
           'discovery_ms': discovery_ms, 'status': 'incomplete'}
    model_ms = tool_ms = 0
    input_tokens = output_tokens = cached_tokens = tool_result_bytes = 0
    final = None
    try:
        for turn in range(1, 19):
            request = {'model': MODEL, 'messages': copy.deepcopy(messages), 'tools': functions,
                       'tool_choice': 'auto', 'temperature': 0, 'max_tokens': 12000, 'stream': False}
            response, elapsed, status = http_json(os.environ['CODE_MODE_LLM_URL'], request,
                {'Authorization': 'Bearer ' + KEY, 'Content-Type': 'application/json'})
            model_ms += elapsed
            run['model'].append({'request': request, 'response': response, 'elapsed_ms': elapsed, 'http_status': status})
            if status != 200:
                raise RuntimeError(f'Model HTTP {status}')
            usage = response['usage']
            input_tokens += usage['prompt_tokens']
            output_tokens += usage['completion_tokens']
            cached_tokens += usage.get('prompt_cache_hit_tokens', usage.get('prompt_tokens_details', {}).get('cached_tokens', 0))
            message = response['choices'][0]['message']
            messages.append(message)
            calls = message.get('tool_calls') or []
            if not calls:
                final = message.get('content', '')
                break
            for call in calls:
                function = call['function']
                allowed = 'execute_code' if mode == 'code' else 'quote_total'
                if function['name'] != allowed:
                    raise ValueError('Unadvertised tool')
                args = json.loads(function['arguments'])
                args = bind(args, f'comparison-{repeat}-{mode}', revision)
                response, event = rpc(mode, 'tools/call', {'name': allowed, 'arguments': args})
                tool_ms += event['elapsed_ms']
                event['model_turn'] = turn
                run['executions'].append(event)
                value = encoded(content(response))
                tool_result_bytes += len(value.encode())
                messages.append({'role': 'tool', 'tool_call_id': call['id'], 'content': value})
            print(f'{repeat} {mode} model turn {turn}, tool calls {len(calls)}, output tokens {usage["completion_tokens"]}', flush=True)
        run['wall_ms'] = (time.perf_counter() - started) * 1000
        if final is None:
            raise ValueError('Model turn limit reached')
        stripped = re.sub(r'^```(?:json)?\s*|\s*```$', '', final.strip())
        run['json_only'] = True
        try:
            answer = json.loads(stripped)
        except json.JSONDecodeError:
            run['json_only'] = False
            blocks = re.findall(r'```(?:json)?\s*(.*?)```', final, re.S)
            if not blocks:
                raise
            answer = json.loads(blocks[-1])
        run['answer'] = answer
        run['answer_correct'] = answer == expected
        run['status'] = 'complete'
    except Exception as error:
        run['wall_ms'] = (time.perf_counter() - started) * 1000
        run['error'] = str(error)
        run['answer_correct'] = False
    backend = metrics()[backend_start:]
    evaluated = set()
    for event in backend:
        for row in event['response'].get('variants', []):
            if 'error' not in row:
                evaluated.add(selection_key(row['selection']))
    run.update(backend=backend, backend_calls=len(backend), distinct_priced=len(evaluated),
               model_calls=len(run['model']), model_tool_calls=len(run['executions']),
               input_tokens=input_tokens, output_tokens=output_tokens, total_tokens=input_tokens+output_tokens,
               cached_input_tokens=cached_tokens, model_ms=model_ms, tool_ms=tool_ms,
               tool_result_bytes=tool_result_bytes, all_priced=evaluated == set(itertools.product(*DIMENSIONS.values())))
    run['verified'] = run['answer_correct'] and run['all_priced']
    print(encoded({k: v for k, v in run.items() if k not in ('discovery', 'model', 'executions', 'backend', 'answer')}), flush=True)
    return run


def main():
    expected = oracle()
    report = {'task': TASK, 'memory': MEMORY, 'expected': expected, 'trials': [],
              'methodology': {'repeats': 3, 'temperature': 0, 'max_tokens': 12000,
                'prompt_revision': 'shared generic bulk-efficiency guidance added after retained exploratory trials',
                'direct_parallel_calls_allowed': True, 'dispatch': 'serialized for both modes',
                'tool_results': 'structured content serialized once for both modes',
                'runtime': 'prewarmed before both modes', 'pricing': 'existing CRM built-in demo rules',
                'code_skill': 'fenced examples removed from model context; original resource and manifest retained',
                'timing': 'wall time includes skill discovery, all model requests and tool execution; excludes server startup, oracle and artifact writes',
                'tokens': 'provider usage summed across requests; cached input included in total and reported separately'}}
    for repeat in range(1, 4):
        order = ('direct', 'code') if repeat % 2 else ('code', 'direct')
        for mode in order:
            report['trials'].append(run_trial(mode, repeat, expected))
            report['verified'] = all(trial['verified'] for trial in report['trials'])
            OUT.write_text(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if report['verified'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
