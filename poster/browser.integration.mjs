// Run explicitly (or through container/test.sh); the unit suite needs no browser.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

test('poster HTTP workflow with an offline browser', { timeout: 360_000 }, async (t) => {
  const dataDir = await mkdtemp(path.join(os.tmpdir(), 'poster-workflow-'));
  let context;
  let server;
  t.after(async () => {
    try {
      await context?.close();
      if (server?.listening) await server[Symbol.asyncDispose]();
    } finally {
      await rm(dataDir, { recursive: true, force: true });
    }
  });
  process.env.DATA_DIR = dataDir;
  process.env.X_USERNAME = 'fixture_account';
  process.env.X_AUTH_TOKEN = 'offline-test';
  process.env.X_PASSWORD = '';
  process.env.HEADLESS = 'true';
  process.env.ELEVENLABS_API_KEY = 'offline-elevenlabs';
  const srt = '1\n00:00:00,000 --> 00:00:02,500\nNieuws uit West-Brabant.\n\n';
  let failTranscription = false;
  const realFetch = globalThis.fetch;
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    if (url === 'https://api.elevenlabs.io/v1/speech-to-text') {
      assert.equal(options.headers['xi-api-key'], 'offline-elevenlabs');
      assert.equal(options.body.get('language_code'), 'nld');
      assert.ok(options.body.get('file').size > 0);
      return new globalThis.Response(JSON.stringify({ additional_formats: [{
        requested_format: 'srt', is_base64_encoded: false, content: srt,
      }] }), { status: failTranscription ? 503 : 200 });
    }
    assert.equal(new URL(url).hostname, '127.0.0.1', 'no external HTTP in the offline test');
    return realFetch(url, options);
  });

  const poster = await import('./server.mjs');
  server = poster.server;
  context = await poster.launch();
  const fixture = (name) => readFile(new URL(`../testdata/contract/${name}`, import.meta.url), 'utf8');
  const pageHTML = await fixture('browser-page.html');
  let rejectPost = false;
  let failPagination = false;
  let failVideo = false;
  let videoStatusRequested;
  let videoProcessing;
  let uploadedBytes = 0;
  let uploadedCaptions;
  let captionsRequested;
  let captionsAccepted;
  const published = [];
  const unexpected = [];
  await context.route('**/*', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== 'https://x.com') {
      unexpected.push(request.url());
      return route.abort();
    }
    if (url.pathname === '/offline/captions') {
      uploadedCaptions = request.postData();
      captionsRequested.resolve();
      await captionsAccepted.promise;
      return route.fulfill({ status: 200, body: '' });
    }
    if (url.pathname === '/i/media/upload.json') {
      const command = url.searchParams.get('command');
      if (command === 'APPEND') {
        uploadedBytes = request.postDataBuffer().length;
        return route.fulfill({ status: 204 });
      }
      const body = { media_id_string: '123' };
      if (command === 'FINALIZE') body.processing_info = { state: 'pending' };
      if (command === 'STATUS') {
        videoStatusRequested.resolve();
        await videoProcessing.promise;
        body.processing_info = failVideo
          ? { state: 'failed', error: { message: 'Synthetic encoding failure' } }
          : { state: 'succeeded' };
      }
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
    }
    if (url.pathname.endsWith('/CreateTweet')) {
      published.push(request.postDataJSON().text);
      return route.fulfill({
        contentType: 'application/json',
        body: await fixture(rejectPost ? 'create-tweet-without-id.json' : 'create-tweet-with-id.json'),
      });
    }
    if (url.pathname.endsWith('/UserOriginalsTimeline')) {
      const continuation = JSON.parse(url.searchParams.get('variables')).cursor;
      if (failPagination && continuation) {
        return route.fulfill({ status: 429, body: 'rate limited' });
      }
      return route.fulfill({
        contentType: 'application/json',
        headers: failPagination ? { 'x-offline-next': 'true' } : {},
        body: await fixture(failPagination ? 'user-tweets-page-1.json' : 'user-tweets-end.json'),
      });
    }
    if (['/home', '/fixture_account'].includes(url.pathname)) {
      return route.fulfill({ contentType: 'text/html', body: pageHTML });
    }
    unexpected.push(request.url());
    return route.abort();
  });

  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const base = `http://127.0.0.1:${server.address().port}`;
  const post = (text, signal) => fetch(`${base}/post`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ text }), signal,
  });
  const postVideo = (text, bytes, signal) => fetch(`${base}/post-video`, {
    method: 'POST',
    headers: { 'content-type': 'video/mp4', 'x-post-text': Buffer.from(text).toString('base64') },
    body: bytes,
    signal,
  });

  await t.test('validates requests before browser work and keeps readiness passive', async () => {
    assert.equal((await fetch(`${base}/ready`)).status, 503);
    assert.equal((await fetch(`${base}/health`)).status, 200);
    assert.equal((await post(' ')).status, 400);
    assert.equal((await fetch(`${base}/post-video`, { method: 'POST' })).status, 400);
    assert.equal((await postVideo(' ', Buffer.from('video'))).status, 400);
    const localPath = await fetch(`${base}/post`, {
      method: 'POST', headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ text: 'invalid', videoFile: '/etc/passwd' }),
    });
    assert.equal(localPath.status, 400);
    for (const hours of [0, -1, 337, 'invalid']) {
      assert.equal((await fetch(`${base}/recent?hours=${hours}`)).status, 400);
    }
    assert.equal(context.pages()[0].url(), 'about:blank');
    const response = await fetch(`${base}/recent?hours=336`);
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), { posts: [], complete: true });
    assert.equal((await fetch(`${base}/ready`)).status, 200);
  });

  await t.test('serializes simultaneous posts and returns confirmed URLs', async () => {
    const before = published.length;
    const responses = await Promise.all([post('First'), post('Second')]);
    for (const response of responses) {
      assert.equal(response.status, 200);
      assert.equal((await response.json()).url, 'https://x.com/fixture_account/status/900000000000000601');
    }
    assert.deepEqual(published.slice(before).sort(), ['First', 'Second']);
  });

  await t.test('a client disconnect during composition prevents publication', async () => {
    const before = [...published];
    const controller = new AbortController();
    const response = post('Cancelled', controller.signal);
    // Attach the rejection handler before aborting to avoid an unhandled rejection.
    const aborted = assert.rejects(response, { name: 'AbortError' });
    const page = context.pages()[0];
    await page.locator('[role=dialog]:not([hidden])').waitFor();
    controller.abort();
    await aborted;
    // This request queues behind the abandoned post; completion proves cleanup ran.
    const recent = await fetch(`${base}/recent?hours=1`);
    assert.equal(recent.status, 200);
    assert.deepEqual(published, before);
  });

  await t.test('a rejection after the actual click is reported as uncertain', async () => {
    rejectPost = true;
    const response = await post('Rejected');
    assert.equal(response.status, 500);
    const body = await response.json();
    assert.equal(body.clicked, true);
    assert.match(body.error, /Synthetic rejection/);
    assert.equal(published.at(-1), 'Rejected');
    rejectPost = false;
  });

  await t.test('binary videos larger than the JSON limit wait for encoding before posting', async () => {
    videoStatusRequested = Promise.withResolvers();
    videoProcessing = Promise.withResolvers();
    captionsRequested = Promise.withResolvers();
    captionsAccepted = Promise.withResolvers();
    const before = [...published];
    const bytes = Buffer.alloc(21 * 1024 * 1024, 1);
    const response = postVideo('Video café 🎥', bytes);
    await videoStatusRequested.promise;
    assert.equal(uploadedBytes, bytes.length);
    assert.deepEqual(published, before, 'a preview and enabled button do not mean the video is ready');
    videoProcessing.resolve();
    await captionsRequested.promise;
    assert.equal(uploadedCaptions, srt);
    assert.deepEqual(published, before, 'caption selection alone must not allow publication');
    captionsAccepted.resolve();
    const result = await response;
    assert.equal(result.status, 200);
    assert.match((await result.json()).url, /\/status\//);
    assert.equal(published.at(-1), 'Video café 🎥');
  });

  await t.test('failed transcription never opens the composer or publishes', async () => {
    failTranscription = true;
    const before = [...published];
    const response = await postVideo('No subtitles', Buffer.from('synthetic MP4'));
    assert.equal(response.status, 500);
    const result = await response.json();
    assert.equal(result.clicked, false);
    assert.match(result.error, /ElevenLabs transcription returned HTTP 503/);
    assert.deepEqual(published, before);
    failTranscription = false;
  });

  await t.test('disconnect while captions load prevents publication', async () => {
    videoStatusRequested = Promise.withResolvers();
    videoProcessing = Promise.withResolvers();
    videoProcessing.resolve();
    captionsRequested = Promise.withResolvers();
    captionsAccepted = Promise.withResolvers();
    const before = [...published];
    const controller = new AbortController();
    const response = postVideo('Cancelled captions', Buffer.from('synthetic MP4'), controller.signal);
    const aborted = assert.rejects(response, { name: 'AbortError' });
    await captionsRequested.promise;
    controller.abort();
    await aborted;
    captionsAccepted.resolve();
    // Queue behind cleanup to prove the abandoned composer never publishes.
    assert.equal((await fetch(`${base}/recent?hours=1`)).status, 200);
    assert.deepEqual(published, before);
  });

  await t.test('failed video processing never clicks Post', async () => {
    failVideo = true;
    videoStatusRequested = Promise.withResolvers();
    videoProcessing = Promise.withResolvers();
    videoProcessing.resolve();
    const before = [...published];
    const response = await postVideo('Bad video', Buffer.from('synthetic MP4'));
    assert.equal(response.status, 500);
    const result = await response.json();
    assert.equal(result.clicked, false);
    assert.match(result.error, /Synthetic encoding failure/);
    assert.deepEqual(published, before);
    failVideo = false;
  });

  await t.test('an HTTP failure in pagination refuses the whole recent result', async () => {
    failPagination = true;
    const response = await fetch(`${base}/recent?hours=48`);
    assert.equal(response.status, 500);
    assert.match((await response.json()).error, /UserTweets returned HTTP 429/);
  });
  assert.deepEqual(unexpected, [], 'all browser requests must use the offline fixture');
});
