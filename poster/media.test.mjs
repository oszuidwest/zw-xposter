import assert from 'node:assert/strict';
import { EventEmitter, on } from 'node:events';
import { mkdtempDisposable, readFile, readdir } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Readable } from 'node:stream';
import { setTimeout as sleep } from 'node:timers/promises';
import test from 'node:test';
import { MAX_VIDEO_BYTES, receiveVideo, uploadImage, uploadVideo, videoPostText } from './media.mjs';

test('video text preserves Unicode and rejects invalid headers', () => {
  const text = 'Nieuws: café 🎥 https://example.nl/';
  assert.equal(videoPostText(Buffer.from(text).toString('base64')), text);
  for (const invalid of [undefined, '', ' ', '!!!', 'IA==', '/w==']) {
    assert.throws(() => videoPostText(invalid));
  }
});

test('binary video storage is bounded and cleans up success and failure', async () => {
  assert.equal(MAX_VIDEO_BYTES, 512 << 20);
  await using directory = await mkdtempDisposable(path.join(os.tmpdir(), 'video-storage-test-'));
  const tempDir = directory.path;
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
  const stalled = Readable.from((async function* () { await sleep(50); yield Buffer.from('video'); })());
  await assert.rejects(receiveVideo(stalled, { tempDir, timeoutMs: 10 }), { stage: 'video' });
  assert.deepEqual(await readdir(tempDir), []);
});

function uploadFixture({ selectError, image = false } = {}) {
  const page = new EventEmitter();
  // Like Playwright: settle on a truthy or throwing predicate, timeout or abort, then stop listening.
  page.waitForResponse = (predicate, { timeout, signal }) => Promise.race([
    (async () => {
      for await (const [res] of on(page, 'response', { signal })) {
        if (await predicate(res)) return res;
      }
    })(),
    sleep(timeout, undefined, { signal }).then(() => { throw new Error('timeout'); }),
  ]);
  const selected = Promise.withResolvers();
  let previews = 0;
  const dialog = {
    locator(selector) {
      return { first: () => ({
        async setInputFiles(file) {
          assert.equal(selector, 'input[data-testid="fileInput"]');
          if (image) assert.equal(file.buffer.toString(), 'image');
          else assert.equal(file, '/tmp/fixture.mp4');
          selected.resolve();
          if (selectError) throw selectError;
        },
        async waitFor() {
          assert.equal(selector, `[data-testid="attachments"] ${image ? 'img' : 'video'}`);
          previews++;
        },
      }) };
    },
  };
  const emit = (body, { status = 200, command = 'STATUS', host = 'upload.x.com', form = false } = {}) => page.emit('response', {
    url: () => `https://${host}/i/media/upload.json${form ? '' : `?command=${command}`}`,
    status: () => status,
    ok: () => status < 400,
    json: async () => body,
    request: () => ({
      headers: () => (form ? { 'content-type': 'application/x-www-form-urlencoded' } : {}),
      postData: () => `command=${command}&media_id=123`,
    }),
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

test('image uploads require server confirmation, not just a preview', async () => {
  const f = uploadFixture({ image: true });
  const upload = uploadImage(f.page, f.dialog, { mime: 'image/png', data: Buffer.from('image').toString('base64') });
  await f.selected;
  assert.equal(f.previews(), 0);
  f.emit({ media_id_string: 'image-123' }, { command: '' });
  await upload;
  assert.equal(f.previews(), 1);
});

test('account and unknown API errors preserve their non-media classification', async (t) => {
  for (const status of [401, 403, 200]) await t.test(`HTTP ${status}`, async () => {
    const f = uploadFixture();
    const upload = uploadVideo(f.page, f.dialog, '/tmp/fixture.mp4');
    const rejected = assert.rejects(upload, { stage: status === 200 ? 'service' : 'session' });
    await f.selected;
    f.emit({ errors: [{ code: 326, message: 'account restricted' }] }, { status });
    await rejected;
  });
});

test('synchronous FINALIZE succeeds without processing_info', async (t) => {
  for (const form of [false, true]) {
    await t.test(form ? 'form body' : 'query string', async () => {
      const f = uploadFixture();
      const upload = uploadVideo(f.page, f.dialog, '/tmp/fixture.mp4', { timeoutMs: 1000 });
      await f.selected;
      f.emit({ media_id_string: '123' }, { command: 'INIT', form });
      f.emit({ media_id_string: '123' }, { command: 'FINALIZE', form });
      await upload;
    });
  }
});

test('upload errors, failed encoding, timeout and cancellation prevent posting', async (t) => {
  for (const mode of ['http', 'encoding', 'api', 'timeout', 'cancelled', 'file selection']) {
    await t.test(mode, async () => {
      const f = uploadFixture({ selectError: mode === 'file selection' ? new Error('input detached') : undefined });
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
