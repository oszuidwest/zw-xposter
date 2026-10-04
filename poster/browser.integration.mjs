// Run explicitly (or through container/test.sh); the unit suite needs no browser.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

test('poster HTTP workflow with an offline browser', { timeout: 240_000 }, async (t) => {
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

  const poster = await import('./server.mjs');
  server = poster.server;
  context = await poster.launch();
  const fixture = (name) => readFile(new URL(`../testdata/contract/${name}`, import.meta.url), 'utf8');
  const pageHTML = await fixture('browser-page.html');
  let rejectPost = false;
  let failPagination = false;
  const published = [];
  const unexpected = [];
  await context.route('**/*', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== 'https://x.com') {
      unexpected.push(request.url());
      return route.abort();
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

  await t.test('validates requests before browser work and keeps readiness passive', async () => {
    assert.equal((await fetch(`${base}/ready`)).status, 503);
    assert.equal((await fetch(`${base}/health`)).status, 200);
    assert.equal((await post(' ')).status, 400);
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

  await t.test('an HTTP failure in pagination refuses the whole recent result', async () => {
    failPagination = true;
    const response = await fetch(`${base}/recent?hours=48`);
    assert.equal(response.status, 500);
    assert.match((await response.json()).error, /UserTweets returned HTTP 429/);
  });
  assert.deepEqual(unexpected, [], 'all browser requests must use the offline fixture');
});
