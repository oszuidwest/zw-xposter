import { createWriteStream } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { clearTimeout, setTimeout } from 'node:timers';
import { URLSearchParams } from 'node:url';
import { TextDecoder } from 'node:util';

// Must match article.MaxVideoSize. Videos travel as binary, never as JSON/base64.
export const MAX_VIDEO_BYTES = 512 * 1024 * 1024;
export const VIDEO_UPLOAD_TIMEOUT_MS = 10 * 60_000;

export function videoPostText(encoded) {
  if (typeof encoded !== 'string' || !encoded) throw new Error('X-Post-Text is required');
  const bytes = Buffer.from(encoded, 'base64');
  if (bytes.toString('base64') !== encoded) throw new Error('X-Post-Text must be base64');
  const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  if (!text.trim()) throw new Error('text is required');
  return text;
}

export async function receiveVideo(source, { maxBytes = MAX_VIDEO_BYTES, tempDir = os.tmpdir() } = {}) {
  const directory = await mkdtemp(path.join(tempDir, 'xposter-upload-'));
  const file = path.join(directory, 'video.mp4');
  const cleanup = () => rm(directory, { recursive: true, force: true });
  let size = 0;
  const limit = new Transform({
    transform(chunk, encoding, callback) {
      size += chunk.length;
      callback(size > maxBytes ? new Error(`video exceeds ${maxBytes} bytes`) : null, chunk);
    },
  });
  try {
    await pipeline(source, limit, createWriteStream(file, { mode: 0o600, flags: 'wx' }), {
      signal: AbortSignal.timeout(5 * 60_000),
    });
    if (!size) throw new Error('video is empty');
    return { file, cleanup };
  } catch (err) {
    await cleanup();
    throw err;
  }
}

function isMediaUpload(url) {
  return ['upload.x.com', 'upload.twitter.com', 'x.com', 'api.x.com'].includes(url.hostname)
    && /\/(?:i|1\.1)\/media\/upload\.json$/.test(url.pathname);
}

// X can show a preview while it is still encoding the video. Require its upload
// response to confirm processing succeeded before allowing the Post button.
export async function uploadVideo(page, dialog, file, {
  timeoutMs = VIDEO_UPLOAD_TIMEOUT_MS,
  throwIfCancelled = () => {},
} = {}) {
  let resolve;
  let reject;
  const ready = new Promise((yes, no) => { resolve = yes; reject = no; });
  // An upload can fail while setInputFiles is still pending.
  ready.catch(() => {});
  const timer = setTimeout(() => reject(new Error('video upload/processing timed out')), timeoutMs);
  let mediaID;
  const response = async (res) => {
    const url = new URL(res.url());
    if (!isMediaUpload(url)) return;
    try {
      if (!res.ok()) throw new Error(`video upload returned HTTP ${res.status()}`);
      if (res.status() === 204) return; // APPEND acknowledgements have no JSON.
      const body = await res.json();
      if (body.errors?.length || body.error) {
        throw new Error(`video upload rejected: ${JSON.stringify(body.errors || body.error)}`);
      }
      const id = body.media_id_string;
      if (id && mediaID && id !== mediaID) return;
      if (id) mediaID = id;
      const info = body.processing_info;
      if (info?.state === 'failed') {
        throw new Error(`video processing failed: ${JSON.stringify(info.error || info)}`);
      }
      if (info?.state === 'succeeded') resolve();
      let command = url.searchParams.get('command');
      const request = res.request();
      if (!command && request.headers()['content-type']?.startsWith('application/x-www-form-urlencoded')) {
        command = new URLSearchParams(request.postData() || '').get('command');
      }
      // FINALIZE without processing_info means synchronous completion.
      if (command === 'FINALIZE' && id && !info) resolve();
    } catch (err) {
      reject(err);
    }
  };
  page.on('response', response);
  try {
    throwIfCancelled();
    await dialog.locator('input[data-testid="fileInput"]').first().setInputFiles(file, { timeout: timeoutMs });
    await ready;
    throwIfCancelled();
    await dialog.locator('[data-testid="attachments"] video').first().waitFor({ timeout: 30_000 });
  } finally {
    clearTimeout(timer);
    page.off('response', response);
  }
}
