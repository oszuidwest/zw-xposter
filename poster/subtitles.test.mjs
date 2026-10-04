import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { Readable } from 'node:stream';
import test from 'node:test';
import { generateSubtitles } from './subtitles.mjs';

const srt = '1\n00:00:01,200 --> 00:00:03,400\nNieuws uit café West.\n\n';
const transcript = (content = srt, base64 = false) => JSON.stringify({
  additional_formats: [{ requested_format: 'srt', is_base64_encoded: base64, content }],
});

async function fixture(t, handler) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'subtitles-test-'));
  const file = path.join(directory, 'video.mp4');
  await writeFile(file, 'MP4 fixture');
  const server = http.createServer(handler);
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const realFetch = globalThis.fetch;
  t.mock.method(globalThis, 'fetch', (url, options) => {
    assert.equal(url, 'https://api.elevenlabs.io/v1/speech-to-text');
    return realFetch(`http://127.0.0.1:${server.address().port}/transcribe`, options);
  });
  t.after(async () => {
    server.closeAllConnections();
    await server[Symbol.asyncDispose]();
    await rm(directory, { recursive: true, force: true });
  });
  return { directory, file };
}

test('streams the MP4 and saves UTF-8 SRT alongside it for the existing cleanup', async (t) => {
  const f = await fixture(t, async (req, res) => {
    assert.equal(req.url, '/transcribe');
    assert.equal(req.headers['xi-api-key'], 'test-key');
    const form = await new globalThis.Request('http://localhost', {
      method: 'POST', headers: req.headers, body: Readable.toWeb(req), duplex: 'half',
    }).formData();
    assert.equal(form.get('model_id'), 'scribe_v2');
    assert.equal(form.get('language_code'), 'nld');
    assert.equal(form.get('diarize'), 'true');
    assert.equal(form.get('timestamps_granularity'), 'word');
    assert.equal(form.get('tag_audio_events'), 'false');
    assert.equal(JSON.parse(form.get('additional_formats'))[0].format, 'srt');
    assert.equal(await form.get('file').text(), 'MP4 fixture');
    res.end(transcript());
  });
  const file = await generateSubtitles(f.file, { apiKey: 'test-key' });
  assert.equal(file, path.join(f.directory, 'video.nl.srt'));
  assert.equal(await readFile(file, 'utf8'), srt);
});

test('accepts base64 SRT and rejects missing credentials before opening the file', async (t) => {
  await assert.rejects(generateSubtitles('/nonexistent', { apiKey: '' }), /ELEVENLABS_API_KEY/);
  const f = await fixture(t, (req, res) => res.end(transcript(Buffer.from(srt).toString('base64'), true)));
  const file = await generateSubtitles(f.file, { apiKey: 'test-key' });
  assert.equal(await readFile(file, 'utf8'), srt);
});

test('rejects failures, empty/malformed/oversized output, truncation and cancellation', async (t) => {
  const cases = {
    unavailable: (req, res) => res.writeHead(503).end('not ready'),
    unauthorized: (req, res) => res.writeHead(401).end('not authorized'),
    quota: (req, res) => res.writeHead(429).end('quota exceeded'),
    redirect: (req, res) => res.writeHead(302, { location: 'http://untrusted.invalid' }).end(),
    empty: (req, res) => res.end(transcript('')),
    missing: (req, res) => res.end('{}'),
    timing: (req, res) => res.end(transcript(srt.replace('00:00:03,400', '00:00:01,000'))),
    overlap: (req, res) => res.end(transcript(srt + srt.replace('1\n', '2\n'))),
    incomplete: (req, res) => res.end(transcript(srt + '2\n00:00:04,000 --> 00:00:05,000\n')),
    html: (req, res) => res.end('<html>Error</html>'),
    utf8: (req, res) => res.end(Buffer.from([0xff])),
    oversized: (req, res) => res.end(Buffer.alloc(4 * 1024 * 1024 + 1)),
    truncated: (req, res) => { res.writeHead(200, { 'content-length': 10000 }); res.end(srt); },
    timeout: () => {},
  };
  for (const [name, handler] of Object.entries(cases)) {
    await t.test(name, async (t) => {
      const f = await fixture(t, handler);
      await assert.rejects(generateSubtitles(f.file, { apiKey: 'test-key', signal: AbortSignal.timeout(100) }));
      assert.deepEqual(await readdir(f.directory), ['video.mp4']);
    });
  }
});
