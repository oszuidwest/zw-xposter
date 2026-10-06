import { createWriteStream } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { TextDecoder } from 'node:util';

// Must match maxVideoSize in internal/article/video.go.
export const MAX_VIDEO_BYTES = 512 * 1024 * 1024;
const VIDEO_RECEIVE_TIMEOUT_MS = 5 * 60_000;

export function videoPostText(encoded) {
  if (typeof encoded !== 'string' || !encoded) throw new Error('X-Post-Text is required');
  const bytes = Buffer.from(encoded, 'base64');
  if (bytes.toString('base64') !== encoded) throw new Error('X-Post-Text must be base64');
  const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  if (!text.trim()) throw new Error('text is required');
  return text;
}

export async function receiveVideo(source, { maxBytes = MAX_VIDEO_BYTES, tempDir = os.tmpdir(), timeoutMs = VIDEO_RECEIVE_TIMEOUT_MS } = {}) {
  const directory = await mkdtemp(path.join(tempDir, 'xposter-upload-'));
  const file = path.join(directory, 'video.mp4');
  const cleanup = () => rm(directory, { recursive: true, force: true });
  let size = 0;
  const timeout = AbortSignal.timeout(timeoutMs);
  const limit = new Transform({
    transform(chunk, encoding, callback) {
      size += chunk.length;
      callback(size > maxBytes ? Object.assign(new Error(`video exceeds ${maxBytes} bytes`), { stage: 'video' }) : null, chunk);
    },
  });
  try {
    await pipeline(source, limit, createWriteStream(file, { mode: 0o600, flags: 'wx' }), { signal: timeout });
    if (!size) throw Object.assign(new Error('video is empty'), { stage: 'video' });
    return { file, cleanup };
  } catch (error) {
    await cleanup();
    if (timeout.aborted) error.stage = 'video';
    throw error;
  }
}
