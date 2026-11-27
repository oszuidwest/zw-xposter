import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { debugHTML, pruneScreenshots } from './debug.mjs';

test('screenshot pruning removes only files older than the retention period', async () => {
  await using directory = await fs.mkdtempDisposable(path.join(os.tmpdir(), 'xposter-debug-'));
  const oldFile = path.join(directory.path, 'old.png');
  const recentFile = path.join(directory.path, 'recent.png');
  await fs.writeFile(oldFile, 'old');
  await fs.writeFile(recentFile, 'recent');

  const now = Date.parse('2026-10-03T12:00:00.000Z');
  await fs.utimes(oldFile, new Date(now - 15 * 24 * 3600_000), new Date(now - 15 * 24 * 3600_000));
  await fs.utimes(recentFile, new Date(now - 13 * 24 * 3600_000), new Date(now - 13 * 24 * 3600_000));

  await pruneScreenshots(directory.path, { now, retentionMs: 14 * 24 * 3600_000 });

  await assert.rejects(fs.lstat(oldFile), { code: 'ENOENT' });
  assert.equal(await fs.readFile(recentFile, 'utf8'), 'recent');
});

test('debug HTML starts with the URL and drops input values', () => {
  const html = debugHTML('https://x.com/i/flow/login', '<p value="kept"><input name="a>b" placeholder="Enter value=" value="hunter2" type="password">');
  assert.equal(html, '<!-- https://x.com/i/flow/login -->\n<p value="kept"><input name="a>b" placeholder="Enter value=" type="password">');
  // Unclosed <input text in a script took seconds with a scan that ran on to the end.
  const start = Date.now();
  debugHTML('u', '<script>' + '<input '.repeat(50_000));
  assert.ok(Date.now() - start < 1000);
  assert.equal(debugHTML('u', 'x'.repeat(6_000_000)).length, '<!-- u -->\n'.length + 5_000_000);
});
