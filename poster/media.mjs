import { createWriteStream } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { URLSearchParams } from 'node:url';
import { TextDecoder } from 'node:util';
import xUI from './x-ui.json' with { type: 'json' };

// Must match maxVideoSize in internal/article/video.go. Videos travel as binary, never as JSON/base64.
export const MAX_VIDEO_BYTES = 512 * 1024 * 1024;
const VIDEO_RECEIVE_TIMEOUT_MS = 5 * 60_000;
const VIDEO_UPLOAD_TIMEOUT_MS = 10 * 60_000;
const CAPTION_UPLOAD = new RegExp(xUI.captionUploadPattern, 'i');
const CAPTION_DONE = new RegExp(xUI.captionDonePattern, 'i');
const CAPTION_REMOVE = new RegExp(xUI.captionRemovePattern, 'i');
export const CAPTION_ATTACHED = new RegExp(xUI.captionAttachedPattern, 'i');

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
    await pipeline(source, limit, createWriteStream(file, { mode: 0o600, flags: 'wx' }), {
      signal: timeout,
    });
    if (!size) throw Object.assign(new Error('video is empty'), { stage: 'video' });
    return { file, cleanup };
  } catch (err) {
    await cleanup();
    if (timeout.aborted) err.stage = 'video';
    throw err;
  }
}

function isMediaUpload(url) {
  return ['upload.x.com', 'upload.twitter.com', 'x.com', 'api.x.com'].includes(url.hostname)
    && /\/(?:i|1\.1)\/media\/upload\.json$/.test(url.pathname);
}

// The upload command is in the query string or a form-encoded body.
function uploadCommand(url, request) {
  const command = url.searchParams.get('command');
  if (command) return command;
  const form = request.headers()['content-type']?.startsWith('application/x-www-form-urlencoded');
  return form ? new URLSearchParams(request.postData() || '').get('command') : null;
}

export const uploadVideo = uploadMedia;

export function uploadImage(page, dialog, image, options) {
  return uploadMedia(page, dialog, {
    name: image.name || 'image', mimeType: image.mime, buffer: Buffer.from(image.data, 'base64'),
  }, { timeoutMs: 60_000, ...options, kind: 'image' });
}

// X's preview can precede encoding; require confirmed processing success.
async function uploadMedia(page, dialog, file, {
  timeoutMs = VIDEO_UPLOAD_TIMEOUT_MS,
  throwIfCancelled = () => {},
  kind = 'video',
} = {}) {
  throwIfCancelled();
  let mediaID;
  // Aborting stops the wait if selecting the file fails before X responds.
  const release = new AbortController();
  const ready = page.waitForResponse(async (res) => {
    const url = new URL(res.url());
    if (!isMediaUpload(url)) return false;
    if (!res.ok()) {
      const error = new Error(`${kind} upload returned HTTP ${res.status()}`);
      // Authentication/account restrictions must retain the current format.
      if ([401, 403].includes(res.status())) error.stage = 'session';
      throw error;
    }
    if (res.status() === 204) return false; // APPEND acknowledgements have no JSON.
    const body = await res.json();
    if (body.errors?.length || body.error) {
      // Unknown API errors may reflect account restrictions, not unusable media.
      throw Object.assign(new Error(`${kind} upload rejected: ${JSON.stringify(body.errors || body.error)}`), { stage: 'service' });
    }
    const id = body.media_id_string;
    mediaID ||= id;
    if (id && id !== mediaID) return false;
    const info = body.processing_info;
    if (info?.state === 'failed') {
      throw new Error(`${kind} processing failed: ${JSON.stringify(info.error || info)}`);
    }
    // Without processing_info, chunked uploads complete at FINALIZE and simple image uploads in their only response.
    const command = uploadCommand(url, res.request());
    return info?.state === 'succeeded' || Boolean(id && !info && (command === 'FINALIZE' || (kind === 'image' && !command)));
  }, { timeout: timeoutMs, signal: release.signal });
  try {
    // An upload can fail while setInputFiles is still pending.
    await Promise.all([
      dialog.locator('input[data-testid="fileInput"]').first().setInputFiles(file, { timeout: timeoutMs }),
      ready,
    ]);
    throwIfCancelled();
    await dialog.locator(`[data-testid="attachments"] ${kind === 'video' ? 'video' : 'img'}`).first().waitFor({ timeout: 30_000 });
  } finally {
    release.abort();
  }
}

export async function uploadSubtitles(page, dialog, srt, { throwIfCancelled = () => {} } = {}) {
  if (!srt) throw new Error('video subtitles are required');
  throwIfCancelled();
  await dialog.getByRole('button', { name: CAPTION_UPLOAD }).click();
  const captions = page.locator('[role="dialog"][aria-modal="true"]').filter({
    has: page.getByRole('button', { name: CAPTION_DONE }),
  });
  await captions.locator('input[type="file"][accept*=".srt"]').setInputFiles({
    name: 'video.nl.srt', mimeType: 'application/x-subrip', buffer: Buffer.from(srt),
  });
  await captions.getByRole('button', { name: CAPTION_REMOVE }).waitFor({ timeout: 60_000 });
  throwIfCancelled();
  await captions.getByRole('button', { name: CAPTION_DONE }).click();
  // X replaces the upload action with the language or its generic captions label.
  await dialog.getByText(CAPTION_ATTACHED).waitFor({ timeout: 60_000 });
  throwIfCancelled();
}
