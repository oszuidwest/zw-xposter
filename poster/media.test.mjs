import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Readable } from 'node:stream';
import { setTimeout as sleep } from 'node:timers/promises';
import test from 'node:test';
import { MAX_VIDEO_BYTES, receiveVideo, uploadVideo, videoPostText } from './media.mjs';

test('video text preserves Unicode and rejects invalid headers', () => {
  const text = 'Nieuws: café 🎥 https://example.nl/';
  assert.equal(videoPostText(Buffer.from(text).toString('base64')), text);
  for (const invalid of [undefined, '', ' ', '!!!', 'IA==', '/w==']) {
    assert.throws(() => videoPostText(invalid));
  }
});

test('binary video storage is bounded and cleans up success and failure', async (t) => {
  assert.equal(MAX_VIDEO_BYTES, 512 << 20);
  const tempDir = await mkdtemp(path.join(os.tmpdir(), 'video-storage-test-'));
  t.after(() => rm(tempDir, { recursive: true, force: true }));
  const video = await receiveVideo(Readable.from([Buffer.from('123'), Buffer.from('456')]), { maxBytes: 6, tempDir });
  assert.equal(await readFile(video.file, 'utf8'), '123456');
  await video.cleanup();
  for (const source of [
    Readable.from([]),
    Readable.from([Buffer.alloc(7)]),
    Readable.from((async function* () { yield Buffer.from('12'); throw new Error('download interrupted'); })()),
  ]) {
    await assert.rejects(receiveVideo(source, { maxBytes: 6, tempDir }));
    assert.deepEqual(await readdir(tempDir), []);
  }
});

function uploadFixture() {
  const page = new EventEmitter();
  const selected = Promise.withResolvers();
  let previews = 0;
  const dialog = {
    locator(selector) {
      return { first: () => ({
        async setInputFiles(file) {
          assert.equal(selector, 'input[data-testid="fileInput"]');
          assert.equal(file, '/tmp/fixture.mp4');
          selected.resolve();
        },
        async waitFor() {
          assert.equal(selector, '[data-testid="attachments"] video');
          previews++;
        },
      }) };
    },
  };
  const emit = (body, { status = 200, command = 'STATUS', host = 'upload.x.com' } = {}) => page.emit('response', {
    url: () => `https://${host}/i/media/upload.json?command=${command}`,
    status: () => status,
    ok: () => status < 400,
    json: async () => body,
    request: () => ({ headers: () => ({}) }),
  });
  return { page, dialog, selected: selected.promise, emit, previews: () => previews };
}

test('video upload waits for processing success, ignoring previews and other media', async () => {
  const f = uploadFixture();
  let finished = false;
  const upload = uploadVideo(f.page, f.dialog, '/tmp/fixture.mp4', { timeoutMs: 1000 });
  upload.then(() => { finished = true; });
  await f.selected;
  f.emit({ media_id_string: '123' }, { command: 'INIT' });
  f.emit(null, { status: 204, command: 'APPEND' });
  f.emit({ media_id_string: '123', processing_info: { state: 'pending' } }, { command: 'FINALIZE' });
  f.emit({ media_id_string: 'other', processing_info: { state: 'succeeded' } });
  f.emit({ media_id_string: '123', processing_info: { state: 'succeeded' } }, { host: 'untrusted.invalid' });
  await sleep(10);
  assert.equal(finished, false);
  assert.equal(f.previews(), 0);
  f.emit({ media_id_string: '123', processing_info: { state: 'succeeded' } });
  await upload;
  assert.equal(f.previews(), 1);
  assert.equal(f.page.listenerCount('response'), 0);
});

test('synchronous FINALIZE succeeds without processing_info', async () => {
  const f = uploadFixture();
  const upload = uploadVideo(f.page, f.dialog, '/tmp/fixture.mp4', { timeoutMs: 1000 });
  await f.selected;
  f.emit({ media_id_string: '123' }, { command: 'FINALIZE' });
  await upload;
});

test('upload errors, failed encoding, timeout and cancellation prevent posting', async (t) => {
  for (const mode of ['http', 'encoding', 'api', 'timeout', 'cancelled']) {
    await t.test(mode, async () => {
      const f = uploadFixture();
      const upload = uploadVideo(f.page, f.dialog, '/tmp/fixture.mp4', {
        timeoutMs: mode === 'timeout' ? 20 : 1000,
        throwIfCancelled: () => { if (mode === 'cancelled') throw new Error('client gone'); },
      });
      const rejected = assert.rejects(upload);
      if (mode !== 'cancelled') await f.selected;
      if (mode === 'http') f.emit({}, { status: 400 });
      if (mode === 'encoding') f.emit({ processing_info: { state: 'failed', error: { message: 'bad codec' } } });
      if (mode === 'api') f.emit({ errors: [{ message: 'too long' }] });
      await rejected;
      assert.equal(f.previews(), 0);
      assert.equal(f.page.listenerCount('response'), 0);
    });
  }
});
