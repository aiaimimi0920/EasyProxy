import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const args = process.argv.slice(2);
if (args.length !== 4) process.exit(1);
let requestPath = null, resultPath = null;
for (let i = 0; i < args.length; i += 2) {
  if (args[i] === '-RequestPath' && typeof args[i + 1] === 'string' && args[i + 1] && requestPath === null) requestPath = args[i + 1];
  else if (args[i] === '-ResultPath' && typeof args[i + 1] === 'string' && args[i + 1] && resultPath === null) resultPath = args[i + 1];
  else process.exit(1);
}
if (!requestPath || !resultPath) process.exit(1);
const root = dirname(fileURLToPath(import.meta.url));
const child = spawnSync('python3.12', ['-B', '-s', join(root, 'bootstrap.py'), '--request', requestPath, '--result', resultPath], {
  stdio: 'inherit',
  env: { ...process.env, PYTHONDONTWRITEBYTECODE: '1', PYTHONNOUSERSITE: '1', PYTHONUTF8: '1' },
});
process.exit(typeof child.status === 'number' ? child.status : 1);
