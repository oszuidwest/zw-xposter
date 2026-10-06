import assert from 'node:assert/strict';
import { once } from 'node:events';
import { access, readFile } from 'node:fs/promises';
import { setImmediate as immediate } from 'node:timers/promises';
import test from 'node:test';
import { createPosterServer } from './server.mjs';
import { httpFixture, jsonFixture, reply } from './http-fixtures.mjs';

async function fixture(t, handler, transcribe) {
  const f = httpFixture(handler);
  const app = createPosterServer({ client: f.client, transcribe, logger: () => {} });
  app.server.listen(0, '127.0.0.1');
  await once(app.server, 'listening');
  t.after(() => app.close());
  const base = `http://127.0.0.1:${app.server.address().port}`;
  const request = async (path, json, options = {}) => {
    const response = await fetch(`${base}${path}`, { ...(json ? { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(json) } : {}), ...options });
    return { status: response.status, body: await response.json() };
  };
  return { ...f, ...app, request, base };
}
test('local HTTP contract: passive health, dry run, pagination, image post and exact deletion', async (t) => {
  const f = await fixture(t);
  assert.equal((await f.request('/health')).status, 200);
  assert.equal((await f.request('/ready')).status, 503);
  assert.equal((await f.request('/post', { text: 'dry', dryRun: true })).body.dryRun, true);
  assert.equal(f.calls.length, 0);
  assert.deepEqual((await f.request('/recent?hours=3')).body, await jsonFixture('recent-complete'));
  f.client.recent = async (hours) => { assert.equal(hours, 336); return { complete: true, posts: [] }; };
  assert.equal((await f.request('/recent?hours=336')).status, 200);
  assert.equal((await f.request('/ready')).status, 200);
  const result = await f.request('/post', { text: 'café 🎥', image: { mime: 'image/jpeg', data: Buffer.from('image').toString('base64') } });
  assert.equal(result.status, 200);
  assert.equal(result.body.id, '900000000000000601');
  const create = f.calls.find((c) => c.url.pathname.endsWith('/CreateTweet'));
  assert.equal(JSON.parse(create.body).variables.tweet_text, 'café 🎥');
  assert.equal(JSON.parse(create.body).variables.media.media_entities[0].media_id, '201');
  assert.deepEqual((await f.request('/delete', { id: result.body.id })).body, { deleted: result.body.id });
  assert.equal(JSON.parse(f.calls.at(-1).body).variables.tweet_id, result.body.id);
});
test('binary video is streamed to disk, captioned, published and removed from disk', async (t) => {
  let file;
  const f = await fixture(t, undefined, async (path, { skip }) => {
    file = path;
    assert.equal(await readFile(path, 'utf8'), 'fixture-video');
    return skip ? { captions: 'none' } : { captions: 'nl', srt: '1\n00:00:00,000 --> 00:00:01,000\nTest\n' };
  });
  const options = { method: 'POST', headers: { 'content-type': 'video/mp4', 'x-post-text': Buffer.from('video 🎥').toString('base64') }, body: 'fixture-video' };
  const response = await f.request('/post-video', null, options);
  assert.equal(response.status, 200);
  assert.equal(response.body.captions, 'nl');
  await immediate();
  await assert.rejects(access(file), { code: 'ENOENT' });
  assert.equal(f.calls.filter((c) => c.url.pathname.endsWith('/subtitles/create.json')).length, 1);
  const without = await f.request('/post-video', null, { ...options, headers: { ...options.headers, 'x-post-captions': 'none' } });
  assert.equal(without.body.captions, 'none');
  assert.equal(f.calls.filter((c) => c.url.pathname.endsWith('/subtitles/create.json')).length, 1);
});
test('invalid inputs never reach X and recent window is bounded', async (t) => {
  const f = await fixture(t);
  for (const [path, payload] of [['/post', { text: '' }], ['/post', { text: 'x', videoFile: '/etc/passwd' }], ['/post', { text: 'x', image: { mime: 'image/jpeg', data: 'bad' } }], ['/delete', { id: 'not-an-id' }], ['/recent?hours=337'], ['/recent?hours=NaN']]) assert.equal((await f.request(path, payload)).status, 400);
  assert.equal((await f.request('/post-video', null, { method: 'POST', body: 'bad' })).status, 400);
  assert.equal(f.calls.length, 0);
});
test('CreateTweet failures are uncertain while upload failures remain pre-publication', async (t) => {
  for (const create of [true, false]) await t.test(create ? 'create' : 'upload', async (t) => {
    const f = await fixture(t, (url) => (create ? url.pathname.endsWith('/CreateTweet') : url.searchParams.get('command') === 'FINALIZE') && reply({ error: 'rejected' }, 500));
    const response = await f.request('/post', { text: 'test', image: { mime: 'image/jpeg', data: 'aW1hZ2U=' } });
    assert.equal(response.status, 500);
    assert.equal(response.body.clicked, create);
    assert.equal(response.body.stage, create ? 'service' : 'image');
    assert.equal(f.calls.filter((c) => c.url.pathname.endsWith('/CreateTweet')).length, create ? 1 : 0);
  });
});
test('account rejection blocks writes; health polling does not touch X or extend backoff', async (t) => {
  const f = await fixture(t, (url) => url.pathname.endsWith('/CreateTweet') && reply({ error: 'unauthorized' }, 401));
  const response = await f.request('/post', { text: 'test' });
  assert.equal(response.body.stage, 'session');
  const count = f.calls.length;
  const before = await f.request('/ready');
  assert.equal(before.status, 503);
  assert.equal((await f.request('/post', { text: 'test' })).body.clicked, false);
  const after = await f.request('/ready');
  assert.equal(after.body.loginBlockedUntil, before.body.loginBlockedUntil);
  assert.equal(f.calls.length, count);
});
test('HTTP 200 automation rejection enters backoff and never falls back or retries', async (t) => {
  const f = await fixture(t, (url) => url.pathname.endsWith('/CreateTweet') && reply({ errors: [{ code: 226, message: 'request looks automated' }] }));
  const result = await f.request('/post', { text: 'test' });
  assert.equal(result.body.clicked, true);
  assert.equal(result.body.stage, 'session');
  assert.equal((await f.request('/ready')).status, 503);
  assert.equal((await f.request('/post', { text: 'test' })).body.clicked, false);
  assert.equal(f.calls.filter((c) => c.url.pathname.endsWith('/CreateTweet')).length, 1);
});
test('disconnected queued request cannot publish; serial queue remains usable', async (t) => {
  const entered = Promise.withResolvers(), release = Promise.withResolvers();
  let creations = 0;
  const f = await fixture(t, async (url) => {
    if (url.pathname.endsWith('/CreateTweet') && ++creations === 1) { entered.resolve(); await release.promise; }
  });
  const first = f.request('/post', { text: 'first' });
  await entered.promise;
  const cancel = new AbortController();
  const second = f.request('/post', { text: 'cancelled' }, { signal: cancel.signal });
  const rejected = assert.rejects(second);
  await immediate();
  cancel.abort();
  await rejected;
  // Wait until the local response socket has observed the disconnect.
  await immediate();
  release.resolve();
  assert.equal((await first).status, 200);
  assert.equal((await f.request('/post', { text: 'third' })).status, 200);
  assert.equal(creations, 2);
});
