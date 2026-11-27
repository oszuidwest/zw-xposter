import fs from 'node:fs/promises';
import path from 'node:path';

export async function pruneScreenshots(directory, { now = Date.now(), retentionMs }) {
  const cutoff = now - retentionMs;
  for (const name of await fs.readdir(directory)) {
    const file = path.join(directory, name);
    if ((await fs.stat(file)).mtimeMs < cutoff) await fs.rm(file, { force: true });
  }
}

const MAX_HTML_CHARS = 5_000_000;

// React copies typed text into the value attribute, so page.content() would hold the password.
// Chromium escapes < in attribute values, so a tag never spans one; stopping there keeps the scan linear.
export function debugHTML(url, html) {
  const safe = html.replace(/(<input\b(?:[^<>"]|"[^<"]*")*?)\svalue="[^"]*"/g, '$1');
  return `<!-- ${url} -->\n${safe.slice(0, MAX_HTML_CHARS)}`;
}
