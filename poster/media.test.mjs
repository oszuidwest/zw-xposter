import assert from 'node:assert/strict';
import { mkdtempDisposable, readFile, readdir } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Readable } from 'node:stream';
import { setTimeout as sleep } from 'node:timers/promises';
import test from 'node:test';
import { MAX_VIDEO_BYTES, receiveVideo, videoPostText } from './media.mjs';

test('video text preserves Unicode and rejects invalid headers', () => {
  const text = 'Nieuws: café 🎥 https://example.nl/';
  assert.equal(videoPostText(Buffer.from(text).toString('base64')), text);
  for (const invalid of [undefined, '', ' ', '!!!', 'IA==', '/w==']) assert.throws(() => videoPostText(invalid));
});

test('binary video storage is bounded and cleans up success and failure', async () => {
  assert.equal(MAX_VIDEO_BYTES, 512 << 20);
  await using directory = await mkdtempDisposable(path.join(os.tmpdir(), 'video-storage-test-'));
  const tempDir = directory.path;
  const video = await receiveVideo(Readable.from([Buffer.from('123'), Buffer.from('456')]), { maxBytes: 6, tempDir });
  assert.equal(await readFile(video.file, 'utf8'), '123456');
  await video.cleanup();
  for (const source of [Readable.from([]), Readable.from([Buffer.alloc(7)]), Readable.from((async function* () { yield Buffer.from('12'); throw new Error('download interrupted'); })())]) {
    await assert.rejects(receiveVideo(source, { maxBytes: 6, tempDir }));
    assert.deepEqual(await readdir(tempDir), []);
  }
  const stalled = Readable.from((async function* () { await sleep(50); yield Buffer.from('video'); })());
  await assert.rejects(receiveVideo(stalled, { tempDir, timeoutMs: 10 }), { stage: 'video' });
  assert.deepEqual(await readdir(tempDir), []);
});
