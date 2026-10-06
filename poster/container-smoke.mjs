// Offline check of the actual runtime image: no credentials or X requests.
import assert from 'node:assert/strict';
import { access, readFile } from 'node:fs/promises';
import { once } from 'node:events';
import { createPosterServer } from './server.mjs';

assert.notEqual(process.getuid(), 0, 'the poster must run as a non-root user');
assert.match(await readFile('/etc/os-release', 'utf8'), /VERSION_ID="26\.04"/);
for (const executable of ['/usr/bin/npm', '/usr/bin/npx', '/usr/local/bin/npm', '/usr/local/bin/npx', '/usr/bin/Xvfb', '/ms-playwright', '/app/poster/node_modules']) await assert.rejects(access(executable), { code: 'ENOENT' });
const app = createPosterServer({ client: {}, logger: () => {} });
try {
  app.server.listen(0, '127.0.0.1');
  await once(app.server, 'listening');
  const base = `http://127.0.0.1:${app.server.address().port}`;
  assert.equal((await fetch(`${base}/health`)).status, 200);
  assert.equal((await fetch(`${base}/ready`)).status, 503);
  const response = await fetch(`${base}/post`, { method: 'POST', body: JSON.stringify({ text: 'offline', dryRun: true }) });
  assert.deepEqual(await response.json(), { dryRun: true });
} finally { await app.close(); }
console.log('Non-root HTTP poster works without npm, Playwright, Chromium or Xvfb');
