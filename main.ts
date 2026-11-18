// Entrypoint: launch the statically linked forge binary vendored in
// this repository. The binary owns the whole lifecycle (bind an
// ephemeral port, register it with the suite, serve until the suite
// reports done); this launcher only spawns it, forwards stdio and
// termination signals, and propagates its exit code.

import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const dir = dirname(fileURLToPath(import.meta.url));
const binary = join(dir, 'forge');

const child = spawn(binary, [], { stdio: 'inherit' });

child.on('error', (err) => {
  process.stderr.write(`[forge] cannot start the vendored binary: ${err.message}\n`);
  process.exit(1);
});

child.on('close', (code, signal) => {
  if (signal !== null) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});

for (const sig of ['SIGTERM', 'SIGINT']) {
  process.on(sig, () => {
    child.kill(sig);
  });
}
