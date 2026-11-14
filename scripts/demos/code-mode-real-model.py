#!/usr/bin/env python3
"""Run a compiled MCP real-model test against an isolated, demo-priced CRM."""
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import time
import urllib.request


def main():
    run = Path(__file__).resolve().parent
    home = Path.home()
    profile = json.loads((home / 'golive-telephony/configs/telephony.json').read_text())
    model = profile['backend_model']
    endpoint = profile['backend_base_url'].rstrip('/') + '/chat/completions'
    if profile['llm'] != 'deepseek':
        raise ValueError('This demo launcher expects the configured DeepSeek profile')
    credentials = {}
    for line in (home / 'golive/.env.local').read_text().splitlines():
        match = re.match(r'^\s*(?:export\s+)?(DEEPSEEK_API_KEY)\s*=\s*(.*)$', line)
        if match:
            credentials[match[1]] = match[2].strip().strip('\"\'')
    key = credentials['DEEPSEEK_API_KEY']
    data = run / 'isolated-crm'
    data.mkdir(exist_ok=False)
    with socket.socket() as probe:
        probe.bind(('127.0.0.1', 0))
        port = probe.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    env = {name: os.environ[name] for name in ('PATH', 'HOME', 'TZ') if name in os.environ}
    with (run / 'crm-process.log').open('w') as log:
        crm = subprocess.Popen([str(run / 'crm-demo'), '-dir', str(data), '-addr', f'127.0.0.1:{port}',
                                '-originator', 'http://127.0.0.1:1', '-originator-env', '/dev/null',
                                '-golive', '', '-quote-api', ''], env=env, stdout=log, stderr=log)
        try:
            for _ in range(100):
                if crm.poll() is not None:
                    raise RuntimeError('Isolated CRM exited during startup')
                try:
                    with urllib.request.urlopen(base + '/healthz', timeout=1):
                        break
                except OSError:
                    time.sleep(.1)
            else:
                raise RuntimeError('Isolated CRM did not become healthy')
            token_path = data / 'api.token'
            request = urllib.request.Request(base + '/api/configs/quote-api',
                headers={'Authorization': 'Bearer ' + token_path.read_text().strip()})
            with urllib.request.urlopen(request, timeout=5) as response:
                config = json.load(response)['config']['body']
            if isinstance(config, str):
                config = json.loads(config)
            if config.get('base_url'):
                raise RuntimeError('Isolated CRM unexpectedly has an external pricing provider')
            env.update(CODE_MODE_REAL_MODEL=model, CODE_MODE_LLM_KEY=key, CODE_MODE_LLM_URL=endpoint,
                       CODE_MODE_CRM_URL=base + '/api/tools/quote', CODE_MODE_CRM_TOKEN_FILE=str(token_path),
                       CODE_MODE_CATALOG_FILE=str(home / 'golive-skills-mcp/data/catalog.json'),
                       GOLIVE_CODE_MODE_DEMO_TRACE=str(run / 'trace.json'))
            print(json.dumps({'model': model, 'pricing': 'isolated CRM built-in demo rules'}), flush=True)
            result = subprocess.run([str(run / 'catalog-real.test'), '-test.run', '^TestCodeModeRealModel$',
                                     '-test.v', '-test.timeout', '12m'], env=env, timeout=730)
            if (run / 'trace.json').exists():
                trace = json.loads((run / 'trace.json').read_text())
                print(json.dumps({name: trace.get(name) for name in ('verified', 'checks', 'backend_calls')},
                                 ensure_ascii=False), flush=True)
            return result.returncode
        finally:
            crm.terminate()
            try:
                crm.wait(timeout=5)
            except subprocess.TimeoutExpired:
                crm.kill()
                crm.wait()


if __name__ == '__main__':
    raise SystemExit(main())
