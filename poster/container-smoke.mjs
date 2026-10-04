// Offline check of the actual runtime image: no credentials or X requests.
import assert from 'node:assert/strict';
import { access, mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { chromium } from 'playwright-core';

assert.notEqual(process.getuid(), 0, 'the browser must run as a non-root user');
assert.match(await readFile('/etc/os-release', 'utf8'), /VERSION_ID="26\.04"/);
for (const executable of ['/usr/bin/npm', '/usr/bin/npx', '/usr/local/bin/npm', '/usr/local/bin/npx']) {
  await assert.rejects(access(executable), { code: 'ENOENT' });
}
const browsers = await readdir(process.env.PLAYWRIGHT_BROWSERS_PATH);
assert.ok(!browsers.some((name) => /firefox|webkit|chromium_headless_shell/.test(name)));

const profile = await mkdtemp(path.join(os.tmpdir(), 'poster-smoke-'));
let context;
try {
  context = await chromium.launchPersistentContext(profile, {
    channel: 'chromium',
    headless: false,
    viewport: null,
    args: ['--use-angle=gl', '--ignore-gpu-blocklist'],
  });
  const page = await context.newPage();
  await page.setContent('<title>Container smoke test</title><button>Ready</button>');
  assert.equal(await page.title(), 'Container smoke test');
  await page.getByRole('button', { name: 'Ready' }).click();
  console.log(`Headed Chromium ${context.browser().version()} works without npm`);
} finally {
  await context?.close();
  await rm(profile, { recursive: true, force: true });
}
