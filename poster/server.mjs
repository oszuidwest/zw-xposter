// Local HTTP contract for the Go orchestrator; X transport lives in x-client.mjs.
import http from 'node:http';
import { setTimeout as sleep } from 'node:timers/promises';
import { XClient } from './x-client.mjs';
import { publish } from './publication.mjs';
import { MAX_VIDEO_BYTES, receiveVideo, videoPostText } from './media.mjs';
import { sessionCheckDelay, sessionHealth } from './health.mjs';
import { postErrorResponse } from './timeline.mjs';

const MAX_RECENT_HOURS = 336;
const POST_TIMEOUT_MS = 4 * 60_000;
const VIDEO_POST_TIMEOUT_MS = 24 * 60_000;
const LOGIN_RETRY_MS = 30 * 60_000;
const log = (...args) => console.log(new Date().toISOString(), ...args);

function send(res, status, body) {
  if (!res.destroyed) res.writeHead(status, { 'content-type': 'application/json' }).end(JSON.stringify(body));
}

async function readJSON(req) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 20 * 1024 * 1024) throw new Error('request body too large');
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

export function createPosterServer({ client, transcribe, logger = log } = {}) {
  let queue = Promise.resolve();
  let loggedIn = false, sessionCheckedAt = 0, blockedUntil = 0;
  const shutdown = new AbortController();
  const exclusive = (fn) => { const run = queue.then(fn); queue = run.catch(() => {}); return run; };
  const requestSignal = (res, timeoutMs) => {
    const controller = new AbortController();
    if (res.destroyed) controller.abort();
    else res.once('close', () => controller.abort());
    return AbortSignal.any([controller.signal, shutdown.signal, AbortSignal.timeout(timeoutMs)]);
  };
  async function authenticated(signal, force = false) {
    signal.throwIfAborted();
    if (blockedUntil > Date.now()) throw Object.assign(new Error('X session/account is in backoff; wait, or update X_AUTH_TOKEN and restart if expired'), { stage: 'session' });
    try {
      await client.ensureSession(signal, { force });
      loggedIn = true; sessionCheckedAt = Date.now(); blockedUntil = 0;
    } catch (error) {
      loggedIn = false; sessionCheckedAt = Date.now();
      if (error.stage === 'session') blockedUntil = Date.now() + LOGIN_RETRY_MS;
      throw error;
    }
  }
  async function post(res, payload, videoFile) {
    const signal = requestSignal(res, videoFile ? VIDEO_POST_TIMEOUT_MS : POST_TIMEOUT_MS);
    const result = await exclusive(async () => {
      if (!payload.dryRun) await authenticated(signal);
      return publish(client, payload, signal, videoFile, transcribe);
    });
    logger(result.dryRun ? 'dry run completed' : 'posted', result.url || '', result.fallbackReason || '');
    return result;
  }
  const server = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://localhost');
      if (req.method === 'GET' && url.pathname === '/health') return send(res, 200, {});
      if (req.method === 'GET' && url.pathname === '/ready') {
        const status = sessionHealth({ loggedIn, checkedAt: sessionCheckedAt, blockedUntil });
        return send(res, status.ready ? 200 : 503, status.body);
      }
      if (req.method === 'GET' && url.pathname === '/recent') {
        const hours = Number(url.searchParams.get('hours') || 48);
        if (!(hours > 0 && hours <= MAX_RECENT_HOURS)) return send(res, 400, { error: `hours must be between 0 and ${MAX_RECENT_HOURS}` });
        const signal = requestSignal(res, POST_TIMEOUT_MS);
        return send(res, 200, await exclusive(async () => { await authenticated(signal); return client.recent(hours, signal); }));
      }
      if (req.method === 'POST' && url.pathname === '/post') {
        const payload = await readJSON(req);
        if (!payload || payload.videoFile) return send(res, 400, { error: 'use /post-video to upload a video' });
        if (typeof payload.text !== 'string' || !payload.text.trim()) return send(res, 400, { error: 'text is required' });
        if (payload.image && (!['image/jpeg', 'image/png', 'image/webp', 'image/gif'].includes(payload.image.mime) || typeof payload.image.data !== 'string' || !payload.image.data)) return send(res, 400, { error: 'image needs a supported mime and base64 data' });
        if (payload.image && Buffer.from(payload.image.data, 'base64').toString('base64') !== payload.image.data) return send(res, 400, { error: 'invalid base64 image' });
        return send(res, 200, await post(res, payload));
      }
      if (req.method === 'POST' && url.pathname === '/delete') {
        const { id } = await readJSON(req);
        if (typeof id !== 'string' || !/^\d{1,25}$/.test(id)) return send(res, 400, { error: 'id must be a numeric post ID' });
        const signal = requestSignal(res, POST_TIMEOUT_MS);
        await exclusive(async () => { await authenticated(signal); await client.deletePost(id, signal); });
        logger('deleted post', id);
        return send(res, 200, { deleted: id });
      }
      if (req.method === 'POST' && url.pathname === '/post-video') {
        let text;
        try {
          if (req.headers['content-type'] !== 'video/mp4') throw new Error('video/mp4 is required');
          if (Number(req.headers['content-length']) > MAX_VIDEO_BYTES) throw new Error('video is too large');
          text = videoPostText(req.headers['x-post-text']);
          if (req.headers['x-post-captions'] && req.headers['x-post-captions'] !== 'none') throw new Error('X-Post-Captions must be none or absent');
        } catch (error) { return send(res, 400, postErrorResponse(error)); }
        const video = await receiveVideo(req);
        let result;
        try { result = await post(res, { text, skipCaptions: req.headers['x-post-captions'] === 'none' }, video.file); }
        finally { await video.cleanup().catch((error) => logger('video cleanup failed:', error.message)); }
        return send(res, 200, result);
      }
      send(res, 404, { error: 'not found' });
    } catch (error) {
      if (error.stage === 'session') { loggedIn = false; sessionCheckedAt = Date.now(); if (blockedUntil <= Date.now()) blockedUntil = Date.now() + LOGIN_RETRY_MS; }
      logger('error:', error.message);
      send(res, 500, postErrorResponse(error));
    }
  });
  return {
    server,
    checkSession: () => exclusive(() => authenticated(AbortSignal.any([shutdown.signal, AbortSignal.timeout(POST_TIMEOUT_MS)]), true)),
    async close() { shutdown.abort(); server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)); await queue; },
  };
}

if (import.meta.main) {
  const client = new XClient({ username: process.env.X_USERNAME, authToken: process.env.X_AUTH_TOKEN, userAgent: process.env.X_USER_AGENT });
  const app = createPosterServer({ client });
  const stop = new AbortController();
  app.server.listen(Number(process.env.PORT || 8081), '127.0.0.1', () => log('HTTP poster listening on', app.server.address().port));
  (async () => {
    while (!stop.signal.aborted) {
      try { await app.checkSession(); log('X session ready'); }
      catch (error) { log('session check failed:', error.message); }
      await sleep(sessionCheckDelay(), undefined, { signal: stop.signal });
    }
  })().catch((error) => { if (!stop.signal.aborted) log('session checker stopped:', error.message); });
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, async () => { stop.abort(); await app.close(); });
}
