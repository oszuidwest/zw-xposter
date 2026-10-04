import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtempDisposable, writeFile } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { generateSubtitles, prepareSubtitles } from './subtitles.mjs';

const srt = '1\n00:00:01,200 --> 00:00:03,400\nNieuws uit café West.\n\n';
const transcript = (content = srt, base64 = false) => JSON.stringify({
  additional_formats: [{ requested_format: 'srt', is_base64_encoded: base64, content }],
});

test('optional captions downgrade failures but preserve caller cancellation', async (t) => {
  await using directory = await mkdtempDisposable(path.join(os.tmpdir(), 'optional-subtitles-'));
  const file = path.join(directory.path, 'video.mp4');
  await writeFile(file, 'video');
  await t.test('absent key, persisted downgrade and dry run make no paid request', async (t) => {
    t.mock.method(globalThis, 'fetch', () => { throw new Error('unexpected paid request'); });
    assert.deepEqual(await prepareSubtitles('/nonexistent', { apiKey: '' }), { captions: 'none', fallbackReason: 'ElevenLabs key absent' });
    assert.deepEqual(await prepareSubtitles('/nonexistent', { skip: true, apiKey: 'key' }), { captions: 'none' });
    assert.deepEqual(await prepareSubtitles('/nonexistent', { dryRun: true, apiKey: 'key' }), { captions: 'unverified' });
  });
  for (const status of [401, 429, 503]) await t.test(`HTTP ${status}`, async (t) => {
    t.mock.method(globalThis, 'fetch', async () => new globalThis.Response('', { status }));
    const result = await prepareSubtitles(file, { apiKey: 'key' });
    assert.equal(result.captions, 'none');
    assert.match(result.fallbackReason, new RegExp(`HTTP ${status}`));
  });
  for (const content of ['', 'invalid', undefined]) await t.test(`unusable SRT ${content}`, async (t) => {
    t.mock.method(globalThis, 'fetch', async () => new globalThis.Response(content === undefined ? '{}' : transcript(content)));
    assert.equal((await prepareSubtitles(file, { apiKey: 'key' })).captions, 'none');
  });
  for (const caller of [false, true]) await t.test(caller ? 'caller cancellation escapes' : 'dedicated timeout downgrades', async (t) => {
    const received = Promise.withResolvers();
    const server = http.createServer(() => received.resolve());
    t.after(async () => { server.closeAllConnections(); await server[Symbol.asyncDispose](); });
    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    const realFetch = globalThis.fetch;
    t.mock.method(globalThis, 'fetch', (url, options) => realFetch(`http://127.0.0.1:${server.address().port}`, options));
    const controller = new AbortController();
    const result = prepareSubtitles(file, { apiKey: 'key', signal: controller.signal, timeoutMs: caller ? 5000 : 50 });
    if (caller) {
      const rejected = assert.rejects(result, { name: 'AbortError' });
      await received.promise;
      controller.abort();
      await rejected;
    } else {
      assert.equal((await result).captions, 'none');
      assert.equal(controller.signal.aborted, false);
    }
  });
});

test('generates subtitles and rejects unusable transcripts', async (t) => {
  await using directory = await mkdtempDisposable(path.join(os.tmpdir(), 'subtitles-test-'));
  const file = path.join(directory.path, 'video.mp4');
  await writeFile(file, 'MP4 fixture');
  await assert.rejects(generateSubtitles('/nonexistent', { apiKey: '' }), /ELEVENLABS_API_KEY/);

  for (const base64 of [false, true]) {
    await t.test(base64 ? 'base64 SRT' : 'UTF-8 SRT', async (t) => {
      t.mock.method(globalThis, 'fetch', async (url, { method, headers, body, redirect }) => {
        assert.equal(url, 'https://api.elevenlabs.io/v1/speech-to-text');
        assert.equal(method, 'POST');
        assert.equal(headers['xi-api-key'], 'test-key');
        assert.equal(redirect, 'error');
        const { file: upload, additional_formats: formats, ...settings } = Object.fromEntries(body);
        assert.equal(await upload.text(), 'MP4 fixture');
        assert.deepEqual(settings, {
          model_id: 'scribe_v2', language_code: 'nld', tag_audio_events: 'false',
          diarize: 'true', timestamps_granularity: 'word',
        });
        assert.deepEqual(JSON.parse(formats), [{
          format: 'srt', include_speakers: false, max_characters_per_line: 42,
          max_segment_chars: 84, max_segment_duration_s: 6,
        }]);
        return new globalThis.Response(transcript(base64 ? Buffer.from(srt).toString('base64') : srt, base64));
      });
      assert.equal(await generateSubtitles(file, { apiKey: 'test-key' }), srt);
    });
  }

  const cases = [
    ['empty', transcript(''), /invalid SRT captions/],
    ['missing', '{}', /no SRT captions/],
    ['timing', transcript(srt.replace('00:00:03,400', '00:00:01,000')), /invalid SRT timing/],
    ['too long', transcript(srt.replace('00:00:03,400', '00:20:00,001')), /invalid SRT timing/],
    ['overlap', transcript(srt + srt.replace('1\n', '2\n')), /invalid SRT timing/],
    ['incomplete', transcript(srt + '2\n00:00:04,000 --> 00:00:05,000\n'), /invalid SRT captions/],
    ['utf8', Buffer.from([0xff]), /encoded data/],
    ['oversized', Buffer.alloc(4 * 1024 * 1024 + 1), /transcript is too large/],
  ];
  for (const [name, body, error] of cases) {
    await t.test(name, async (t) => {
      t.mock.method(globalThis, 'fetch', async () => new globalThis.Response(body));
      await assert.rejects(generateSubtitles(file, { apiKey: 'test-key' }), error);
    });
  }

  await t.test('cancels an in-flight transcription', { timeout: 5000 }, async (t) => {
    const received = Promise.withResolvers();
    const server = http.createServer(() => received.resolve());
    t.after(async () => {
      server.closeAllConnections();
      await server[Symbol.asyncDispose]();
    });
    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    const realFetch = globalThis.fetch;
    t.mock.method(globalThis, 'fetch', (url, options) => realFetch(`http://127.0.0.1:${server.address().port}`, options));
    const controller = new AbortController();
    const rejected = assert.rejects(generateSubtitles(file, {
      apiKey: 'test-key', signal: AbortSignal.any([controller.signal, t.signal]),
    }), { name: 'AbortError' });
    await received.promise;
    controller.abort();
    await rejected;
  });
});
