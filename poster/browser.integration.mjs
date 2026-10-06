// Run explicitly (or through container/test.sh); the unit suite needs no browser.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtempDisposable, readFile, readdir } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

test('poster HTTP workflow with an offline browser', { timeout: 900_000 }, async (t) => {
  const cleanup = new AsyncDisposableStack();
  t.after(() => cleanup.disposeAsync());
  process.env.DATA_DIR = cleanup.use(await mkdtempDisposable(path.join(os.tmpdir(), 'poster-workflow-'))).path;
  // Keep received videos inside this fixture so cleanup is observable on every outcome.
  process.env.TMPDIR = process.env.DATA_DIR;
  process.env.X_USERNAME = 'fixture_account';
  process.env.X_AUTH_TOKEN = 'offline-test';
  process.env.X_PASSWORD = '';
  process.env.HEADLESS = 'true';
  const srt = '1\n00:00:00,000 --> 00:00:02,500\nNieuws uit West-Brabant.\n\n';
  let failTranscription;
  let transcriptions = 0;
  const realFetch = globalThis.fetch;
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    if (url === 'https://api.elevenlabs.io/v1/speech-to-text') {
      transcriptions++;
      return new globalThis.Response(JSON.stringify({ additional_formats: [{
        requested_format: 'srt', is_base64_encoded: false, content: srt,
      }] }), { status: failTranscription ? 503 : 200 });
    }
    assert.equal(new URL(url).hostname, '127.0.0.1', 'no external HTTP in the offline test');
    return realFetch(url, options);
  });

  const poster = await import('./server.mjs');
  const server = poster.server;
  cleanup.defer(async () => {
    if (server.listening) await server[Symbol.asyncDispose]();
  });
  const context = cleanup.use(await poster.launch());
  const fixture = (name) => readFile(new URL(`../testdata/contract/${name}`, import.meta.url), 'utf8');
  const pageHTML = await fixture('browser-page.html');
  let rejectPost;
  let failPagination;
  let failVideo;
  let uploadStatus;
  let failCaptions;
  let failCaptionSelection;
  let captionStatus;
  let captionCategory;
  let failImage;
  let failRecovery;
  let loggedOut;
  let videoStatusRequested;
  let videoProcessing;
  let uploadedBytes = 0;
  let uploads = 0;
  let captionsRequested;
  let captionsAccepted;
  let captionSelected;
  let selectionAllowed;
  let holdCaptionSelection;
  await context.exposeBinding('offlineCaptionSelected', () => {
    captionSelected.resolve();
    if (holdCaptionSelection) return selectionAllowed.promise;
  });
  t.beforeEach(() => {
    failTranscription = false;
    rejectPost = false;
    failPagination = false;
    failVideo = false;
    uploadStatus = 200;
    failCaptions = false;
    failCaptionSelection = false;
    captionStatus = 201;
    captionCategory = undefined;
    failImage = false;
    failRecovery = false;
    loggedOut = false;
    process.env.ELEVENLABS_API_KEY = 'offline-elevenlabs';
    videoStatusRequested = Promise.withResolvers();
    videoProcessing = Promise.withResolvers();
    captionsRequested = Promise.withResolvers();
    captionsAccepted = Promise.withResolvers();
    captionSelected = Promise.withResolvers();
    selectionAllowed = Promise.withResolvers();
    holdCaptionSelection = false;
  });
  const published = [];
  const unexpected = [];
  await context.route('**/*', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== 'https://x.com') {
      unexpected.push(request.url());
      return route.abort();
    }
    if (url.pathname === '/i/api/1.1/media/subtitles/create.json') {
      return route.fulfill({ status: 200, body: '' });
    }
    if (url.pathname === '/i/media/upload.json') {
      if (uploadStatus !== 200) return route.fulfill({ status: uploadStatus, body: 'account restricted' });
      const command = url.searchParams.get('command');
      if (url.searchParams.has('media_category')) {
        captionCategory = url.searchParams.get('media_category');
        return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ media_id_string: 'caption-123' }) });
      }
      if (url.searchParams.get('media_id') === 'caption-123') {
        if (command === 'APPEND') {
          assert.equal(request.postData(), srt);
          captionsRequested.resolve();
          await captionsAccepted.promise;
          return route.fulfill({ status: 204 });
        }
        assert.equal(command, 'FINALIZE');
        const rejected = failCaptions || captionCategory !== 'subtitles';
        return route.fulfill({ status: rejected ? 400 : captionStatus, contentType: 'application/json', body: JSON.stringify(rejected
          ? { error: 'media type unrecognized.' }
          : { media_id_string: 'caption-123', subtitles: { subtitle_format: 'text/srt' } }) });
      }
      if (command === 'INIT') uploads++;
      if (!command) return route.fulfill({ status: failImage ? 400 : 200, contentType: 'application/json', body: JSON.stringify({ media_id_string: 'image-123' }) });
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
      const page = context.pages()[0];
      const text = request.postDataJSON().text;
      if (text === 'Caption recovery') {
        assert.equal(await page.locator('[data-testid=attachments] video').count(), 1);
        assert.equal(await page.getByText('Transcripties', { exact: true }).count(), 0);
      }
      if (text === 'Text fallback') assert.equal(await page.locator('[data-testid=attachments] img, [data-testid=attachments] video').count(), 0);
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
      let body = pageHTML;
      body = body.replace("if (file.type !== 'video/mp4') return;", "if (file.type !== 'video/mp4') { await fetch('/i/media/upload.json', { method: 'POST', body: file }); return; }");
      if (failRecovery) body = body.replace('role="dialog" aria-modal="true" hidden', 'role="dialog" aria-modal="true"');
      if (loggedOut) body = body.replace('data-testid="SideNav_AccountSwitcher_Button"', 'data-testid="logged-out"');
      if (failCaptionSelection) body = body.replace("document.querySelector('#caption-remove').hidden = false;", "document.querySelector('#caption-remove').hidden = true;");
      return route.fulfill({ contentType: 'text/html', body });
    }
    unexpected.push(request.url());
    return route.abort();
  });

  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const base = `http://127.0.0.1:${server.address().port}`;
  const post = (text, signal, image) => fetch(`${base}/post`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ text, image }), signal,
  });
  const image = { mime: 'image/png', data: 'aW1hZ2U=' };
  const postVideo = (text, bytes, signal, skipCaptions = false) => fetch(`${base}/post-video`, {
    method: 'POST',
    headers: { 'content-type': 'video/mp4', 'x-post-text': Buffer.from(text).toString('base64'), ...(skipCaptions && { 'x-post-captions': 'none' }) },
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

  for (const video of [false, true]) await t.test(`cancelled ${video ? 'captions' : 'text'} never publishes`, async () => {
    holdCaptionSelection = video;
    videoProcessing.resolve();
    const before = [...published];
    const controller = new AbortController();
    const response = video ? postVideo('Cancelled captions', Buffer.from('synthetic MP4'), controller.signal)
      : post('Cancelled', controller.signal);
    // Attach the rejection handler before aborting to avoid an unhandled rejection.
    const aborted = assert.rejects(response, { name: 'AbortError' });
    await (video ? captionSelected.promise : context.pages()[0].locator('[role=dialog]:not([hidden])').waitFor());
    controller.abort();
    await aborted;
    captionsAccepted.resolve();
    selectionAllowed.resolve();
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
  });

  await t.test('binary videos larger than the JSON limit wait for encoding before posting', async () => {
    const before = [...published];
    const bytes = Buffer.alloc(21 * 1024 * 1024, 1);
    const response = postVideo('Video café 🎥', bytes);
    await videoStatusRequested.promise;
    assert.equal(uploadedBytes, bytes.length);
    assert.deepEqual(published, before, 'a preview and enabled button do not mean the video is ready');
    videoProcessing.resolve();
    await captionsRequested.promise;
    assert.deepEqual(published, before, 'the deferred caption upload must finish before publication');
    captionsAccepted.resolve();
    const result = await response;
    assert.equal(result.status, 200);
    assert.match((await result.json()).url, /\/status\//);
    assert.equal(published.at(-1), 'Video café 🎥');
  });

  await t.test('failed transcription publishes without captions', async () => {
    failTranscription = true;
    videoProcessing.resolve();
    const before = published.length;
    const response = await postVideo('Bad transcript', Buffer.from('synthetic MP4'));
    assert.equal(response.status, 200);
    const result = await response.json();
    assert.equal(result.captions, 'none');
    assert.match(result.fallbackReason, /HTTP 503/);
    assert.equal(published.length, before + 1);
  });

  await t.test('a deferred caption rejection preserves uncertainty and downgrades the next retry', async () => {
    failCaptions = true;
    videoProcessing.resolve();
    captionsAccepted.resolve();
    const before = [...published];
    const response = await postVideo('Late caption failure', Buffer.from('synthetic MP4'));
    const result = await response.json();
    assert.equal(response.status, 500);
    assert.equal(result.clicked, true);
    assert.equal(result.stage, 'captions');
    assert.equal(result.captions, 'none');
    assert.match(result.error, /caption upload FINALIZE returned HTTP 400: media type unrecognized/);
    assert.match(result.fallbackReason, /caption upload/);
    assert.deepEqual(published, before, 'no automatic second publication request after a click');
  });

  await t.test('cancelling the deferred caption upload prevents CreateTweet', async () => {
    videoProcessing.resolve();
    const before = [...published];
    const controller = new AbortController();
    const response = postVideo('Cancelled upload', Buffer.from('synthetic MP4'), controller.signal);
    const aborted = assert.rejects(response, { name: 'AbortError' });
    await captionsRequested.promise;
    controller.abort();
    await aborted;
    captionsAccepted.resolve();
    assert.equal((await fetch(`${base}/recent?hours=1`)).status, 200);
    assert.deepEqual(published, before);
  });

  await t.test('a deferred caption authorization failure preserves the caption format', async () => {
    captionStatus = 403;
    videoProcessing.resolve();
    captionsAccepted.resolve();
    const response = await postVideo('Restricted captions', Buffer.from('synthetic MP4'));
    const result = await response.json();
    assert.equal(response.status, 500);
    assert.equal(result.clicked, true);
    assert.equal(result.stage, 'session');
    assert.equal(result.captions, 'attached');
    assert.equal(result.fallbackReason, undefined);
  });

  await t.test('failed encoding reports video stage', async () => {
    failVideo = true;
    videoProcessing.resolve();
    const before = [...published];
    const response = await postVideo('Bad video', Buffer.from('synthetic MP4'));
    assert.equal(response.status, 500);
    const result = await response.json();
    assert.equal(result.clicked, false);
    assert.equal(result.stage, 'video');
    assert.match(result.error, /Synthetic encoding failure/);
    assert.deepEqual(published, before);
  });

  for (const skip of [false, true]) await t.test(skip ? 'persisted caption downgrade skips transcription' : 'missing key publishes uncaptioned video', async () => {
    if (!skip) delete process.env.ELEVENLABS_API_KEY;
    videoProcessing.resolve();
    const before = transcriptions;
    const response = await postVideo('No captions', Buffer.from('MP4'), undefined, skip);
    assert.equal(response.status, 200);
    assert.equal((await response.json()).captions, 'none');
    assert.equal(transcriptions, before);
  });

  await t.test('caption attachment failure recovers once and reuploads without another transcript', async () => {
    failCaptionSelection = true;
    videoProcessing.resolve();
    captionsAccepted.resolve();
    const before = transcriptions;
    const uploadsBefore = uploads;
    const posts = published.length;
    const response = await postVideo('Caption recovery', Buffer.from('MP4'));
    assert.equal(response.status, 200);
    const result = await response.json();
    assert.equal(result.captions, 'none');
    assert.match(result.fallbackReason, /caption attachment/);
    assert.equal(transcriptions, before + 1);
    assert.equal(uploads, uploadsBefore + 2);
    assert.equal(published.length, posts + 1);
  });

  await t.test('image upload failure permits a clean text request', async () => {
    failImage = true;
    const response = await post('Image fallback', undefined, image);
    const result = await response.json();
    assert.equal(response.status, 500);
    assert.equal(result.stage, 'image');
    assert.equal(result.clicked, false);
    failImage = false;
    assert.equal((await post('Text fallback')).status, 200);
  });

  await t.test('image success waits for the upload response', async () => {
    const response = await post('Image success', undefined, image);
    assert.equal(response.status, 200);
    assert.match((await response.json()).url, /\/status\//);
  });

  await t.test('upload account restrictions do not authorize fallback', async () => {
    uploadStatus = 403;
    const response = await postVideo('Restricted', Buffer.from('MP4'), undefined, true);
    const result = await response.json();
    assert.equal(result.clicked, false);
    assert.equal(result.stage, 'session');
  });

  await t.test('failed composer recovery does not authorize fallback', async () => {
    failVideo = true;
    videoProcessing.resolve();
    const response = postVideo('Unrecoverable', Buffer.from('MP4'));
    await videoStatusRequested.promise;
    failRecovery = true;
    const result = await (await response).json();
    assert.equal(result.clicked, false);
    assert.equal(result.stage, 'service');
  });

  await t.test('login failure does not discard media', async () => {
    loggedOut = true;
    const before = published.length;
    const response = await postVideo('Logged out', Buffer.from('MP4'), undefined, true);
    const result = await response.json();
    assert.equal(result.stage, 'service');
    assert.equal(result.clicked, false);
    assert.equal(published.length, before);
  });

  await t.test('an HTTP failure in pagination refuses the whole recent result', async () => {
    failPagination = true;
    const response = await fetch(`${base}/recent?hours=48`);
    assert.equal(response.status, 500);
    assert.match((await response.json()).error, /UserTweets returned HTTP 429/);
  });
  assert.deepEqual(unexpected, [], 'all browser requests must use the offline fixture');
  assert.deepEqual((await readdir(process.env.DATA_DIR)).filter((name) => name.startsWith('xposter-upload-')), [], 'received videos are released after success, failure and cancellation');
});
