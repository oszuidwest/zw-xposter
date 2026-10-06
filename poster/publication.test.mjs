import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { setTimeout, clearTimeout } from 'node:timers';
import { URLSearchParams } from 'node:url';
import test from 'node:test';
import { waitForPostResponse } from './media.mjs';

function fixture(t, { timeoutMs = 1000 } = {}) {
  const page = new EventEmitter();
  // Playwright invokes async predicates concurrently as responses arrive.
  page.waitForResponse = (predicate, { signal, timeout }) => new Promise((resolve, reject) => {
    let settled = false;
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      page.off('response', listener);
      signal.removeEventListener('abort', abort);
      if (error) reject(error); else resolve(value);
    };
    const listener = async (response) => {
      try { if (await predicate(response)) finish(null, response); } catch (error) { finish(error); }
    };
    const abort = () => finish(signal.reason);
    const timer = setTimeout(() => finish(new Error('timeout')), timeout);
    page.on('response', listener);
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) abort();
  });
  const controller = new AbortController();
  t.after(() => controller.abort());
  const result = waitForPostResponse(page, { signal: controller.signal, timeoutMs });
  result.catch(() => {});
  const request = (url, form) => {
    const req = { url: () => url, headers: () => form ? { 'content-type': 'application/x-www-form-urlencoded' } : {}, postData: () => form };
    page.emit('request', req);
    return req;
  };
  const respond = async (req, { status = 200, body = '', read } = {}) => {
    const response = { url: req.url, request: () => req, status: () => status, ok: () => status >= 200 && status < 300, headers: () => ({ 'content-type': typeof body === 'string' ? 'text/plain' : 'application/json' }), text: read || (async () => typeof body === 'string' ? body : JSON.stringify(body)) };
    page.emit('response', response);
    // Drain async predicate work when the response body is immediately available.
    await Promise.resolve();
    await Promise.resolve();
    return response;
  };
  const upload = async (parameters, options) => respond(request(`https://upload.x.com/i/media/upload.json?${new URLSearchParams(parameters)}`), options);
  const init = () => upload({ command: 'INIT', media_category: 'subtitles' }, { status: 202, body: { media_id_string: 'captions-1' } });
  const finish = () => upload({ command: 'FINALIZE', media_id: 'captions-1' }, { status: 201, body: { media_id_string: 'captions-1', subtitles: { subtitle_format: 'text/srt' } } });
  const publish = () => respond(request('https://x.com/i/api/graphql/test/CreateTweet'), { body: { data: {} } });
  const cleaned = () => {
    assert.equal(page.listenerCount('request'), 0);
    assert.equal(page.listenerCount('response'), 0);
  };
  return { page, controller, result, request, respond, upload, init, finish, publish, cleaned };
}

test('caption upload and association successes still require CreateTweet', async (t) => {
  const f = fixture(t);
  await f.init();
  await f.upload({ command: 'APPEND', media_id: 'captions-1' }, { status: 204 });
  await f.finish();
  await f.respond(f.request('https://x.com/i/api/1.1/media/subtitles/create.json'));
  await f.respond(f.request('https://x.com/i/api/1.1/media/metadata/create.json'));
  const response = await f.publish();
  assert.equal(await f.result, response);
  f.cleaned();
});

test('empty association success does not require a readable response body', async (t) => {
  const f = fixture(t);
  await f.respond(f.request('https://x.com/i/api/1.1/media/subtitles/create.json'), { read: async () => { throw new Error('No data found for resource'); } });
  const response = await f.publish();
  assert.equal(await f.result, response);
  f.cleaned();
});

test('deferred subtitle FINALIZE rejection reports the real X error', async (t) => {
  const f = fixture(t);
  await f.init();
  await f.upload({ command: 'FINALIZE', media_id: 'captions-1' }, { status: 400, body: { error: 'media type unrecognized.' } });
  await assert.rejects(f.result, { stage: 'captions', message: 'X caption upload FINALIZE returned HTTP 400: media type unrecognized.' });
  f.cleaned();
});

test('caption identity survives a slow INIT response body', async (t) => {
  const f = fixture(t);
  const body = Promise.withResolvers();
  await f.respond(f.request('https://upload.x.com/i/media/upload.json?command=INIT&media_category=subtitles'), { status: 202, read: () => body.promise });
  await f.upload({ command: 'FINALIZE', media_id: 'captions-1' }, { status: 400, body: { error: 'invalid captions' } });
  body.resolve(JSON.stringify({ media_id_string: 'captions-1' }));
  await assert.rejects(f.result, { stage: 'captions' });
  f.cleaned();
});

test('recognizes caption INIT failures from query or form parameters', async (t) => {
  for (const form of [false, true]) await t.test(form ? 'form body' : 'query', async (t) => {
    const f = fixture(t);
    const parameters = 'command=INIT&media_category=subtitles';
    const req = f.request(`https://upload.x.com/i/media/upload.json${form ? '' : `?${parameters}`}`, form ? parameters : undefined);
    await f.respond(req, { status: 400, body: { error: 'invalid captions' } });
    await assert.rejects(f.result, { stage: 'captions', message: 'X caption upload INIT returned HTTP 400: invalid captions' });
    f.cleaned();
  });
});

test('subtitle association rejection is a caption failure', async (t) => {
  const f = fixture(t);
  await f.respond(f.request('https://x.com/i/api/1.1/media/subtitles/create.json'), { status: 400, body: { error: 'invalid language' } });
  await assert.rejects(f.result, { stage: 'captions', message: 'X caption association returned HTTP 400: invalid language' });
  f.cleaned();
});

test('account restrictions and ambiguous API errors never downgrade captions', async (t) => {
  for (const status of [401, 403, 200]) await t.test(`HTTP ${status}`, async (t) => {
    const f = fixture(t);
    await f.init();
    await f.upload({ command: 'FINALIZE', media_id: 'captions-1' }, { status, body: { errors: [{ code: 326, message: 'account restricted' }] } });
    await assert.rejects(f.result, { stage: status === 200 ? 'service' : 'session' });
    f.cleaned();
  });
});

test('unrelated media and metadata errors do not discard captions', async (t) => {
  for (const path of ['/i/media/upload.json?command=FINALIZE&media_id=other', '/i/api/1.1/media/metadata/create.json']) await t.test(path, async (t) => {
    const f = fixture(t);
    await f.init();
    await f.respond(f.request(`https://x.com${path}`), { status: 400, body: 'not captions' });
    await assert.rejects(f.result, { stage: 'service' });
    f.cleaned();
  });
});

test('a caption error after CreateTweet has started remains a service error', async (t) => {
  const f = fixture(t);
  await f.init();
  f.request('https://x.com/i/api/graphql/test/CreateTweet');
  await f.upload({ command: 'FINALIZE', media_id: 'captions-1' }, { status: 400, body: 'late error' });
  await assert.rejects(f.result, { stage: 'service' });
  f.cleaned();
});

test('old responses and foreign hosts cannot settle the current publication', async (t) => {
  const f = fixture(t);
  await f.respond({ url: () => 'https://x.com/i/api/graphql/test/CreateTweet' });
  await f.respond(f.request('https://foreign.invalid/i/api/graphql/test/CreateTweet'));
  await f.respond(f.request('https://foreign.invalid/i/api/1.1/media/subtitles/create.json'), { status: 400 });
  const response = await f.publish();
  assert.equal(await f.result, response);
  f.cleaned();
});

test('non-JSON HTTP errors retain status and bounded details', async (t) => {
  const f = fixture(t);
  await f.init();
  await f.upload({ command: 'FINALIZE', media_id: 'captions-1' }, { status: 502, body: 'upstream unavailable '.repeat(1000) });
  await assert.rejects(f.result, (error) => error.stage === 'captions' && /HTTP 502: upstream unavailable/.test(error.message) && error.message.length < 1100);
  f.cleaned();
});

test('timeout and cancellation remove the request observer', async (t) => {
  for (const cancel of [false, true]) await t.test(cancel ? 'cancellation' : 'timeout', async (t) => {
    const f = fixture(t, { timeoutMs: 10 });
    if (cancel) f.controller.abort(new Error('cancelled'));
    await assert.rejects(f.result, cancel ? /cancelled/ : /timeout/);
    f.cleaned();
  });
});
