import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from export import private_dir
from prompt import prompt_for_pair, render_prompt
from select_checkpoint import select


class ExportTests(unittest.TestCase):
    def test_placeholder_ignore_does_not_allow_model_inside_repository(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            (root / '.gitignore').write_text('out/artifact\n')
            with self.assertRaises(ValueError):
                private_dir(str(root / 'out'))

    def test_best_saved_validation_checkpoint_wins_without_test_data(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            run, out = root / 'run', root / 'out'
            (run / 'adapters').mkdir(parents=True)
            out.mkdir()
            (run / 'train.log').write_text('Iter 1: Val loss 8.0\nIter 50: Val loss 2.0\nIter 100: Val loss 3.0\n')
            (run / 'adapters' / '0000050_adapters.safetensors').write_bytes(b'best')
            (run / 'adapters' / 'adapters.safetensors').write_bytes(b'last')
            (run / 'adapters' / 'adapter_config.json').write_text('{}')
            result = select(run, out)
            self.assertEqual(result['iteration'], 50)
            self.assertEqual((out / 'adapters.safetensors').read_bytes(), b'best')

    def test_saved_prompt_options_are_used_for_inference(self):
        pair = {'context': [{'role': 'user', 'content': '测试'},
                            {'role': 'assistant', 'content': '测试回复。'}],
                'caller': '测试问题', 'opener': '好的，',
                'prompt_options': {'max_context_turns': 0, 'max_prompt_chars': 768}}
        self.assertEqual(prompt_for_pair(pair), render_prompt(pair['context'], pair['caller'],
                                                             pair['opener'], max_context_turns=0))


if __name__ == '__main__':
    unittest.main()
