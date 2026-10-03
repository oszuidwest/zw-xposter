import fs from 'node:fs/promises';
import path from 'node:path';

export async function pruneScreenshots(directory, { now = Date.now(), retentionMs }) {
  const cutoff = now - retentionMs;
  for (const name of await fs.readdir(directory)) {
    const file = path.join(directory, name);
    if ((await fs.stat(file)).mtimeMs < cutoff) await fs.rm(file, { force: true });
  }
}
