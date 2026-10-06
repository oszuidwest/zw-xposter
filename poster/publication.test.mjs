import assert from 'node:assert/strict';
import test from 'node:test';
import { publish } from './publication.mjs';

function fixture(fail = {}) {
  const calls = [];
  const controller = new AbortController();
  const client = Object.fromEntries(['ensureSession', 'uploadVideo', 'uploadImage', 'attachSubtitles', 'createPost'].map((method) => [method, async (...args) => {
    calls.push([method, ...args]);
    if (fail[method]) throw fail[method];
    return { id: '123', url: 'https://x.com/fixture/status/123' };
  }]));
  const transcribe = async () => { calls.push(['transcribe']); return { srt: 'captions', captions: 'nl' }; };
  return { client, calls, controller, names: () => calls.map(([name]) => name), run: (payload = {}, video = '/tmp/video') => publish(client, { text: 'test', ...payload }, controller.signal, video, transcribe) };
}

test('captioned publication waits for upload and association before one CreateTweet', async () => {
  const f = fixture();
  assert.equal((await f.run()).captions, 'nl');
  assert.deepEqual(f.names(), ['ensureSession', 'transcribe', 'uploadVideo', 'attachSubtitles', 'createPost']);
});
test('dry run does not authenticate, upload, transcribe or publish', async () => {
  const f = fixture();
  assert.deepEqual(await f.run({ dryRun: true }), { dryRun: true, captions: 'unverified' });
  assert.deepEqual(f.names(), []);
});
test('definite caption rejection reuploads clean video once without retranscription', async () => {
  const f = fixture({ attachSubtitles: Object.assign(new Error('invalid captions'), { stage: 'captions' }) });
  assert.equal((await f.run()).captions, 'none');
  assert.deepEqual(f.names(), ['ensureSession', 'transcribe', 'uploadVideo', 'attachSubtitles', 'uploadVideo', 'createPost']);
});
test('ambiguous or session failures never discard captions or publish', async () => {
  for (const stage of ['service', 'session']) {
    const f = fixture({ attachSubtitles: Object.assign(new Error('unavailable'), { stage }) });
    await assert.rejects(f.run(), { stage, clicked: false, captions: 'nl' });
    assert.equal(f.names().includes('createPost'), false);
  }
});
test('lost publication response is uncertain, never automatically replayed', async () => {
  const f = fixture({ createPost: new Error('connection lost') });
  await assert.rejects(f.run(), { clicked: true, captions: 'nl' });
  assert.equal(f.names().filter((name) => name === 'createPost').length, 1);
});
test('image and text paths omit transcription; cancellation prevents publication', async () => {
  const f = fixture();
  await f.run({ image: { data: 'test' } }, null);
  assert.deepEqual(f.names(), ['ensureSession', 'uploadImage', 'createPost']);
  f.calls.length = 0;
  await f.run({}, null);
  assert.deepEqual(f.names(), ['ensureSession', 'createPost']);
  f.calls.length = 0;
  f.client.uploadVideo = async () => { f.controller.abort(); };
  await assert.rejects(f.run(), { clicked: false });
  assert.equal(f.names().includes('createPost'), false);
});
