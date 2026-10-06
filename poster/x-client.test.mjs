import assert from 'node:assert/strict';
import test from 'node:test';
import { CookieJar, initialState, webOperations } from './x-client.mjs';
import { bundle, home, httpFixture, reply, jsonFixture } from './http-fixtures.mjs';

test('bootstrap parses data without execution and rejects changed bundle schemas', () => {
  assert.equal(initialState(home()).session.user_id, '100000000000000001');
  assert.equal(initialState(`window.__INITIAL_STATE__=${JSON.stringify({ text: '"};window.' })};`).text, '"};window.');
  for (const html of ['<html>login</html>', 'window.__INITIAL_STATE__={']) assert.throws(() => initialState(html));
  assert.equal(webOperations(bundle).operations.CreateTweet.id, 'fixture-CreateTweet');
  assert.throws(() => webOperations(bundle.replaceAll('CreateTweet', 'Changed')));
  assert.throws(() => webOperations('eval("untrusted")'));
});
test('cookies honor host, path, expiry and reject foreign domains', () => {
  const jar = new CookieJar('secret');
  const set = (cookie, url = 'https://x.com/home') => jar.update(reply('', 200, { 'set-cookie': cookie }), url, 1000);
  set('ct0=csrf; Domain=.x.com; Path=/');
  set('host=only; Path=/i/api');
  set('foreign=bad; Domain=evil.invalid');
  set('gone=old; Max-Age=0');
  assert.equal(jar.header('https://abs.twimg.com/'), '');
  assert.equal(jar.header('https://upload.x.com/i/media'), 'auth_token=secret; ct0=csrf');
  assert.equal(jar.header('https://x.com/i/apix'), 'auth_token=secret; ct0=csrf');
  assert.match(jar.header('https://x.com/i/api/test'), /host=only/);
});
test('authenticated session verifies account, caches bundle, never sends secrets to CDN', async () => {
  const { client, calls } = httpFixture();
  await client.ensureSession();
  await client.ensureSession();
  assert.equal(calls.length, 2);
  await client.ensureSession(undefined, { force: true });
  assert.equal(calls.length, 3);
  assert.equal(calls[1].headers.cookie, undefined);
  assert.equal(calls[1].headers.authorization, undefined);
  assert.equal(calls[0].redirect, 'error');
  await client.createPost('test');
  const sent = calls.at(-1);
  assert.equal(sent.headers['x-csrf-token'], 'fixture-csrf');
  assert.equal(sent.headers.authorization, 'Bearer FakePublicToken');
  assert.deepEqual(JSON.parse(sent.body).features, { enabled: true, disabled: false, missing: false });
  const wrong = httpFixture((url) => url.pathname === '/home' && reply(home('wrong'), 200, { 'set-cookie': 'ct0=csrf; Domain=.x.com' }));
  await assert.rejects(wrong.client.ensureSession(), { stage: 'session' });
  assert.equal(wrong.calls.length, 1);
  await assert.rejects(client.request('https://evil.invalid'), /Untrusted/);
  await assert.rejects(client.request('https://abs.twimg.com/script.js'), { stage: 'session' });
});
test('new bootstrap bundle replaces operation IDs and missing CSRF fails closed', async () => {
  let version = 0;
  const { client, calls } = httpFixture((url) => {
    if (url.pathname === '/home') return reply(home(undefined, `version${++version}`), 200, { 'set-cookie': 'ct0=csrf; Domain=.x.com' });
    if (url.hostname === 'abs.twimg.com') return reply(bundle.replace('fixture-CreateTweet', `version${version}`));
  });
  await client.ensureSession();
  await client.ensureSession(undefined, { force: true });
  assert.equal(client.operations.CreateTweet.id, 'version2');
  assert.equal(calls.length, 4);
  await assert.rejects(httpFixture((url) => url.pathname === '/home' && reply(home())).client.ensureSession(), { stage: 'session' });
});
test('timeline follows cursor and fails closed on repeated, missing or malformed pages', async () => {
  const { client, calls } = httpFixture();
  assert.equal((await client.recent(3)).complete, true);
  const pages = calls.filter((c) => c.url.pathname.endsWith('/UserTweets'));
  assert.equal(pages.length, 2);
  assert.equal(JSON.parse(pages[1].url.searchParams.get('variables')).cursor, 'synthetic-page-2');
  for (const name of ['user-tweets-page-1', 'user-tweets-no-cursor', 'user-tweets-pagination-error']) {
    const f = httpFixture(async (url) => url.pathname.endsWith('/UserTweets') && reply(await jsonFixture(name)));
    await assert.rejects(f.client.recent(3));
  }
});
test('upload chunks bytes, waits for processing, associates SRT and uses acknowledged IDs', async () => {
  let statuses = 0;
  const { client, calls } = httpFixture((url) => {
    const command = url.searchParams.get('command');
    if (command === 'FINALIZE' || command === 'STATUS') return reply({ media_id_string: url.searchParams.get('media_id'), processing_info: { state: command === 'FINALIZE' ? 'pending' : ++statuses ? 'succeeded' : 'in_progress' } });
  });
  await client.ensureSession();
  const media = await client.upload({ data: Buffer.alloc(5 * 1024 * 1024 + 3), type: 'video/mp4', category: 'amplify_video', stage: 'video' });
  const chunks = calls.filter((c) => c.url.searchParams.get('command') === 'APPEND');
  assert.deepEqual(chunks.map((c) => c.body.get('media').size), [5 * 1024 * 1024, 3]);
  assert.deepEqual(chunks.map((c) => c.url.searchParams.get('segment_index')), ['0', '1']);
  assert.equal(statuses, 1);
  await client.attachSubtitles(media, '1\n00:00:00,000 --> 00:00:01,000\nTest');
  const association = JSON.parse(calls.at(-1).body);
  assert.equal(association.media_id, '201');
  assert.equal(association.subtitle_info.subtitles[0].media_id, '202');
  assert.equal(association.subtitle_info.subtitles[0].language_code, 'nl');
});
test('caption failures distinguish definite rejection from transient and account failures', async () => {
  for (const command of ['INIT', 'APPEND', 'FINALIZE', 'association']) for (const status of [400, 401, 403, 408, 429, 500, 503]) {
    const f = httpFixture((url) => (url.searchParams.get('command') === command || command === 'association' && url.pathname.endsWith('/subtitles/create.json')) && reply({ error: 'rejected' }, status));
    await f.client.ensureSession();
    await assert.rejects(f.client.attachSubtitles({ id: '200', category: 'amplify_video' }, 'srt'), { stage: [401, 403].includes(status) ? 'session' : status === 400 ? 'captions' : 'service' });
  }
});
test('ambiguous API errors, missing media IDs and failed processing prevent success', async () => {
  for (const [body, stage] of [[{ errors: [{ code: 326, message: 'restricted' }] }, 'service'], [{ media_id_string: 'wrong' }, 'video'], [{ media_id_string: '201', processing_info: { state: 'failed' } }, 'video']]) {
    const f = httpFixture((url) => url.searchParams.get('command') === 'FINALIZE' && reply(body));
    await f.client.ensureSession();
    await assert.rejects(f.client.upload({ data: 'video', type: 'video/mp4', category: 'amplify_video', stage: 'video' }), { stage });
  }
});
test('transport limits, cancellation, error redaction and no retries', async () => {
  const f = httpFixture((url) => url.pathname.endsWith('/CreateTweet') && reply({ errors: [{ code: 89, message: 'fixture-secret fixture-csrf' }] }, 401));
  await f.client.ensureSession();
  await assert.rejects(f.client.createPost('text'), (e) => e.stage === 'session' && !/fixture-secret|fixture-csrf/.test(e.message));
  assert.equal(f.client.checkedAt, 0);
  assert.equal(f.calls.filter((c) => c.url.pathname.endsWith('/CreateTweet')).length, 1);
  await assert.rejects(f.client.request('https://x.com/home', { authenticated: false, limit: 1 }), /size limit/);
  const count = f.calls.length;
  await assert.rejects(f.client.ensureSession(AbortSignal.abort()));
  assert.equal(f.calls.length, count);
});
test('aborting processing stops polling and GIF uses its own media category', async () => {
  const controller = new AbortController();
  const f = httpFixture((url) => url.searchParams.get('command') === 'FINALIZE' && reply({ media_id_string: '201', processing_info: { state: 'pending' } }), { wait: async () => { controller.abort(); } });
  await f.client.ensureSession();
  await assert.rejects(f.client.uploadImage({ mime: 'image/gif', data: 'aW1hZ2U=' }, controller.signal));
  assert.equal(f.calls.find((c) => c.url.searchParams.get('command') === 'INIT').url.searchParams.get('media_category'), 'tweet_gif');
  assert.equal(f.calls.filter((c) => c.url.searchParams.get('command') === 'STATUS').length, 0);
});
test('lost CreateTweet transport response is not replayed', async () => {
  const f = httpFixture((url) => { if (url.pathname.endsWith('/CreateTweet')) throw new Error('connection reset'); });
  await f.client.ensureSession();
  await assert.rejects(f.client.createPost('test'), /connection reset/);
  assert.equal(f.calls.filter((c) => c.url.pathname.endsWith('/CreateTweet')).length, 1);
});
