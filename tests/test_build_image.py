import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class BuildImageTests(unittest.TestCase):
    def capture(self, script, arguments):
        with tempfile.TemporaryDirectory() as directory:
            capture = Path(directory) / 'commands.jsonl'
            env = dict(os.environ, EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH=str(capture))
            # A harmless command stub satisfies discovery; capture mode prevents execution.
            (Path(directory) / 'docker.cmd').write_text('@exit /b 99\n')
            env['PATH'] = directory + os.pathsep + env['PATH']
            result = subprocess.run(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass',
                                     '-File', str(ROOT / script), *arguments], cwd=ROOT,
                                    env=env, capture_output=True, text=True, timeout=30)
            records = ([json.loads(line) for line in capture.read_text(encoding='utf-8-sig').splitlines()]
                       if capture.exists() else [])
            return result, records

    def test_target_defaults_and_optional_build_flags(self):
        for target, image, dockerfile in [
            ('easyproxy', 'easy-proxy/easy-proxy:local', 'deploy/service/base/Dockerfile'),
            ('ech-workers', 'easyproxy/ech-workers-monorepo:local', 'deploy/upstreams/ech-workers/Dockerfile'),
        ]:
            for custom in (False, True):
                with self.subTest(target=target, custom=custom):
                    extra = ['-Image', 'example/custom:test', '-NoCache', '-Push'] if custom else []
                    result, records = self.capture('scripts/build-image.ps1', ['-Target', target, *extra])
                    self.assertEqual(result.returncode, 0, result.stderr)
                    expected_image = 'example/custom:test' if custom else image
                    expected = ['build', '-f', str(ROOT / dockerfile), '-t', expected_image]
                    if custom:
                        expected.append('--no-cache')
                    expected.append(str(ROOT))
                    self.assertEqual(records[0]['FilePath'], 'docker')
                    self.assertEqual(records[0]['Arguments'], expected)
                    self.assertEqual(len(records), 2 if custom else 1)
                    if custom:
                        self.assertEqual(records[1]['Arguments'], ['push', expected_image])

    def test_dispatcher_retains_project_names_and_passes_target(self):
        for target in ('easyproxy', 'ech-workers'):
            result, records = self.capture('scripts/deploy-subproject.ps1', [
                '-Project', f'build-{target}-image', '-TopologyPath', str(ROOT / 'topology.example.yaml'),
                '-Image', 'example/custom:test', '-NoCache', '-Push'])
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(records), 1)
            self.assertEqual(records[0]['Arguments'], [
                '-ExecutionPolicy', 'Bypass', '-File', str(ROOT / 'scripts/build-image.ps1'),
                '-Target', target, '-Image', 'example/custom:test', '-NoCache', '-Push'])

    def test_unknown_target_fails_before_docker(self):
        result, records = self.capture('scripts/build-image.ps1', ['-Target', 'unknown'])
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(records, [])


if __name__ == '__main__':
    unittest.main()
