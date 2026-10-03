import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { pruneScreenshots } from './debug.mjs';

test('screenshot pruning removes only files older than the retention period', async (t) => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'xposter-debug-'));
  t.after(() => fs.rm(directory, { force: true, recursive: true }));
  const oldFile = path.join(directory, 'old.png');
  const recentFile = path.join(directory, 'recent.png');
  await fs.writeFile(oldFile, 'old');
  await fs.writeFile(recentFile, 'recent');

  const now = Date.parse('2026-10-03T12:00:00.000Z');
  await fs.utimes(oldFile, new Date(now - 15 * 24 * 3600_000), new Date(now - 15 * 24 * 3600_000));
  await fs.utimes(recentFile, new Date(now - 13 * 24 * 3600_000), new Date(now - 13 * 24 * 3600_000));

  await pruneScreenshots(directory, { now, retentionMs: 14 * 24 * 3600_000 });

  await assert.rejects(fs.lstat(oldFile), { code: 'ENOENT' });
  assert.equal(await fs.readFile(recentFile, 'utf8'), 'recent');
});
