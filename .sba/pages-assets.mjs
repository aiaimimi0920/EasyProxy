// Match Cloudflare's hashFile: BLAKE3(base64(contents) + extname without dot),
// first 32 hex characters. Use the exact dependency from the approved npm lock.
import { createRequire } from 'node:module';
import { readFileSync, readdirSync, lstatSync, writeFileSync } from 'node:fs';
import { resolve, join, extname } from 'node:path';
import { createHash } from 'node:crypto';

const require = createRequire(join(process.cwd(), 'package.json'));
const { hash } = require('blake3-wasm');
const root = resolve(process.argv[2]);
const entries = [];
function visit(directory, prefix = '') {
    for (const name of readdirSync(directory).sort()) {
        if (name.startsWith('.') || /[\\\r\n]/.test(name)) throw new Error('Unexpected asset path');
        const file = join(directory, name);
        const relative = prefix + name;
        const stat = lstatSync(file);
        if (stat.isSymbolicLink()) throw new Error('Linked asset');
        if (stat.isDirectory()) { visit(file, relative + '/'); continue; }
        if (!stat.isFile() || stat.size > 25 * 1024 * 1024 || entries.length >= 20000) throw new Error('Asset limit');
        if (['functions', 'node_modules', '_worker.js', '_worker.bundle', '_headers', '_redirects', '_routes.json']
            .includes(relative.split('/')[0])) throw new Error('Unexpected special asset');
        const content = readFileSync(file);
        entries.push({ path: '/' + relative,
            hash: hash(content.toString('base64') + extname(file).substring(1)).toString('hex').slice(0, 32),
            sha256: createHash('sha256').update(content).digest('hex') });
    }
}
visit(root);
writeFileSync(process.argv[3], JSON.stringify(entries), { encoding: 'utf8', flag: 'wx' });
