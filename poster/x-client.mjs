import { openAsBlob } from 'node:fs';
import { setTimeout as sleep } from 'node:timers/promises';
import { createTimelineCollector, parseUserTweetsPayload, parseCreateTweetResponse, parseDeleteTweetResponse } from './timeline.mjs';

const USER_AGENT = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36';
const UPLOAD = 'https://upload.x.com/i/media/upload.json';
const CHUNK_BYTES = 5 * 1024 * 1024;
const SESSION_TTL = 5 * 60_000;
const OPERATIONS = ['CreateTweet', 'DeleteTweet', 'UserTweets'];
const sessionError = (message) => Object.assign(new Error(message), { stage: 'session' });

// Parse data, never execute JavaScript supplied by X. Brace scanning also handles
// semicolons and escaped quotes inside post text in the initial state.
export function initialState(html) {
  const marker = 'window.__INITIAL_STATE__=';
  const position = html.indexOf(marker);
  if (position < 0) throw sessionError('X returned no logged-in initial state');
  const start = position + marker.length;
  let depth = 0, quoted = false, escaped = false;
  for (let i = start; i < html.length; i++) {
    const char = html[i];
    if (quoted) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === '"') quoted = false;
    } else if (char === '"') quoted = true;
    else if (char === '{') depth++;
    else if (char === '}' && --depth === 0) return JSON.parse(html.slice(start, i + 1));
  }
  throw sessionError('X returned incomplete initial state');
}

export function webOperations(source) {
  const bearer = source.match(/"Rb",0,\(\)=>"(Bearer [^"]+)"/)?.[1];
  if (!bearer || !/^Bearer [A-Za-z0-9%]+$/.test(bearer)) throw new Error('X webclient bearer format changed');
  const operations = {};
  for (const name of OPERATIONS) {
    const pattern = new RegExp(`queryId:"([\\w-]+)",operationName:"${name}",operationType:"(?:query|mutation)",metadata:\\{featureSwitches:\\[([^\\]]*)\\],fieldToggles:\\[([^\\]]*)\\]`);
    const match = source.match(pattern);
    if (!match) throw new Error(`X webclient operation ${name} was not found`);
    operations[name] = { id: match[1], features: JSON.parse(`[${match[2]}]`), fields: JSON.parse(`[${match[3]}]`) };
  }
  return { bearer, operations };
}

// Restricted to the two authenticated X hosts; no cookies or credentials are
// forwarded to the public script CDN. Cookies keep their domain/path/expiry.
export class CookieJar {
  constructor(token) { this.cookies = new Map([['x.com|/|auth_token', { name: 'auth_token', value: token, domain: 'x.com', path: '/', expires: Infinity }]]); }
  update(response, url, now = Date.now()) {
    const host = new URL(url).hostname;
    for (const raw of response.headers.getSetCookie()) {
      const [pair, ...attributes] = raw.split(';');
      const split = pair.indexOf('=');
      if (split < 1) continue;
      const cookie = { name: pair.slice(0, split).trim(), value: pair.slice(split + 1), domain: host, hostOnly: true, path: '/', expires: Infinity };
      const attrs = Object.fromEntries(attributes.map((part) => { const [key, ...value] = part.trim().split('='); return [key.toLowerCase(), value.join('=')]; }));
      if (attrs.domain) {
        cookie.domain = attrs.domain.toLowerCase().replace(/^\./, '');
        cookie.hostOnly = false;
        if (cookie.domain !== 'x.com' && cookie.domain !== host) continue;
      }
      if (attrs.path?.startsWith('/')) cookie.path = attrs.path;
      if (attrs.expires) cookie.expires = Date.parse(attrs.expires);
      if (/^-?\d+$/.test(attrs['max-age'])) cookie.expires = now + Number(attrs['max-age']) * 1000;
      this.cookies.set(`${cookie.domain}|${cookie.path}|${cookie.name}`, cookie);
    }
  }
  values(url, now = Date.now()) {
    const { hostname, pathname } = new URL(url);
    return [...this.cookies.values()].filter((c) => c.expires > now
      && (hostname === c.domain || (!c.hostOnly && hostname.endsWith(`.${c.domain}`)))
      && (pathname === c.path || pathname.startsWith(c.path.endsWith('/') ? c.path : `${c.path}/`)));
  }
  header(url) { return this.values(url).map((c) => `${c.name}=${c.value}`).join('; '); }
  csrf() { return this.values('https://x.com/').find((c) => c.name === 'ct0')?.value; }
}

async function readText(response, limit) {
  let size = 0;
  const chunks = [];
  for await (const chunk of response.body || []) {
    size += chunk.length;
    if (size > limit) throw new Error('X response exceeded the size limit');
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString('utf8');
}

export class XClient {
  constructor({ username, authToken, userAgent = USER_AGENT, fetchImpl = fetch, now = Date.now, wait = sleep } = {}) {
    if (!username || !authToken) throw new Error('X_USERNAME and X_AUTH_TOKEN are required');
    this.username = username.replace(/^@/, '');
    this.authToken = authToken;
    this.userAgent = userAgent;
    this.fetch = fetchImpl;
    this.now = now;
    this.wait = wait;
    this.jar = new CookieJar(authToken);
    this.checkedAt = 0;
  }

  async request(url, { signal, method = 'GET', body, json, authenticated = true, limit = 4 * 1024 * 1024, timeoutMs = 60_000 } = {}) {
    const target = new URL(url);
    const xHost = ['x.com', 'upload.x.com'].includes(target.hostname);
    if (target.protocol !== 'https:' || target.port || target.username || target.password || (!xHost && target.hostname !== 'abs.twimg.com')) throw new Error('Untrusted X request URL');
    if (authenticated && (!xHost || !this.bearer || !this.jar.csrf())) throw sessionError('No authenticated X session');
    signal?.throwIfAborted();
    const headers = { 'user-agent': this.userAgent, accept: '*/*' };
    if (xHost) headers.cookie = this.jar.header(url);
    if (authenticated) Object.assign(headers, { authorization: this.bearer, 'x-csrf-token': this.jar.csrf(), 'x-twitter-auth-type': 'OAuth2Session', 'x-twitter-active-user': 'yes', 'x-twitter-client-language': 'nl', origin: 'https://x.com', referer: 'https://x.com/' });
    if (json !== undefined) { headers['content-type'] = 'application/json'; body = JSON.stringify(json); }
    try {
      const response = await this.fetch(url, { method, headers, body, redirect: 'error', signal: AbortSignal.any([...(signal ? [signal] : []), AbortSignal.timeout(timeoutMs)]) });
      if (xHost) this.jar.update(response, url, this.now());
      const text = await readText(response, limit);
      let data;
      try { data = JSON.parse(text); } catch { /* HTML bootstrap and empty APPEND responses are expected. */ }
      if (!response.ok || data?.errors?.length || data?.error) {
        // 226 is an explicit automation block. Stop writes and expose unready
        // rather than treating it as a media failure or immediately retrying.
        const authFailure = [401, 403].includes(response.status) || data?.errors?.some((e) => [32, 89, 99, 215, 226, 353].includes(e.code));
        if (authFailure) this.checkedAt = 0;
        const detail = data?.errors?.map((e) => `${e.code ?? ''} ${e.message ?? ''}`).join('; ') || data?.error?.message || (typeof data?.error === 'string' ? data.error : 'request rejected');
        // Never echo HTML, cookies, request bodies or a remote reflection of secrets.
        const safe = String(detail).replaceAll(this.authToken, '[redacted]').replaceAll(this.jar.csrf() || '\0', '[redacted]').slice(0, 300);
        throw Object.assign(new Error(`X ${target.pathname} returned HTTP ${response.status}: ${safe}`), { status: response.status, stage: authFailure ? 'session' : 'service', apiErrors: Boolean(data?.errors?.length) });
      }
      return { data, text, status: response.status };
    } catch (error) {
      if (!error.stage) error.stage = 'service';
      throw error;
    }
  }

  async ensureSession(signal, { force = false } = {}) {
    signal?.throwIfAborted();
    if (!force && this.checkedAt && this.now() - this.checkedAt < SESSION_TTL) return;
    this.checkedAt = 0;
    const { text: html } = await this.request('https://x.com/home', { authenticated: false, signal, limit: 8 * 1024 * 1024 });
    const state = initialState(html);
    const ownId = state.session?.user_id;
    const user = state.entities?.users?.entities?.[ownId];
    const handle = user?.screen_name || user?.core?.screen_name;
    if (!/^\d+$/.test(ownId) || handle?.toLowerCase() !== this.username.toLowerCase() || !this.jar.csrf()) throw sessionError('X session is expired or belongs to a different account; update X_AUTH_TOKEN');
    const script = [...html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)].map((m) => m[1]).find((url) => /^https:\/\/abs\.twimg\.com\/responsive-web\/client-web\/main\.[\w]+\.js$/.test(url));
    if (!script) throw new Error('X main bundle URL was not found');
    if (script !== this.script) {
      const { text } = await this.request(script, { authenticated: false, signal, limit: 16 * 1024 * 1024 });
      Object.assign(this, webOperations(text));
      this.script = script;
    }
    if (!state.featureSwitch?.defaultConfig) throw new Error('X feature configuration was not found');
    this.config = { ...state.featureSwitch.defaultConfig, ...state.featureSwitch.user?.config, ...state.featureSwitch.customOverrides };
    this.ownId = ownId;
    this.checkedAt = this.now();
  }

  async graphql(name, variables, signal) {
    const operation = this.operations?.[name];
    if (!operation) throw new Error(`Missing X operation ${name}`);
    // X's isTrue uses strict equality; absent flags are false.
    const features = Object.fromEntries(operation.features.map((key) => [key, (this.config[key]?.value ?? this.config[key]) === true]));
    const fieldToggles = Object.fromEntries(operation.fields.map((key) => [key, false]));
    const url = `https://x.com/i/api/graphql/${operation.id}/${name}`;
    if (name === 'UserTweets') {
      const query = new URLSearchParams({ variables: JSON.stringify(variables), features: JSON.stringify(features), fieldToggles: JSON.stringify(fieldToggles) });
      return (await this.request(`${url}?${query}`, { signal })).data;
    }
    return (await this.request(url, { method: 'POST', json: { variables, features, fieldToggles, queryId: operation.id }, signal })).data;
  }

  async recent(hours, signal) {
    await this.ensureSession(signal);
    const options = { ownId: this.ownId, username: this.username, cutoff: this.now() - hours * 3600_000 };
    const collector = createTimelineCollector(options);
    const seen = new Set();
    let cursor;
    for (let page = 0; page < 100; page++) {
      const body = await this.graphql('UserTweets', { userId: this.ownId, count: 100, includePromotedContent: false, withQuickPromoteEligibilityTweetFields: true, withVoice: true, ...(cursor ? { cursor } : {}) }, signal);
      collector.add(body, { firstPage: page === 0 });
      if (collector.complete()) return collector.result();
      const next = parseUserTweetsPayload(body, options).bottomCursor;
      if (!next || seen.has(next)) break;
      seen.add(next); cursor = next;
    }
    throw new Error(`Incomplete X timeline: ${collector.incompleteReason()}`);
  }

  async upload({ file, data, type, category, stage }, signal) {
    const timeout = AbortSignal.timeout(10 * 60_000);
    const uploadSignal = AbortSignal.any([timeout, ...(signal ? [signal] : [])]);
    const url = (params) => `${UPLOAD}?${new URLSearchParams(params)}`;
    try {
      const blob = file ? await openAsBlob(file, { type }) : new Blob([data], { type });
      if (!blob.size || blob.size > 512 * 1024 * 1024) throw new Error('Invalid media size');
      const { data: init } = await this.request(url({ command: 'INIT', total_bytes: String(blob.size), media_type: type, media_category: category }), { method: 'POST', signal: uploadSignal });
      const id = init?.media_id_string;
      if (!/^\d{1,25}$/.test(id)) throw new Error('X upload INIT returned no media ID');
      for (let offset = 0, segment = 0; offset < blob.size; offset += CHUNK_BYTES, segment++) {
        const body = new FormData();
        body.set('media', blob.slice(offset, offset + CHUNK_BYTES, type), 'media');
        await this.request(url({ command: 'APPEND', media_id: id, segment_index: String(segment) }), { method: 'POST', body, signal: uploadSignal });
      }
      let { data: final } = await this.request(url({ command: 'FINALIZE', media_id: id, allow_async: 'true' }), { method: 'POST', signal: uploadSignal });
      for (;;) {
        if (final?.media_id_string !== id) throw new Error('X upload returned an unexpected media ID');
        const processing = final.processing_info;
        if (!processing || processing.state === 'succeeded') return { id, key: init.media_key, category };
        if (processing.state === 'failed') throw Object.assign(new Error('X media processing failed'), { stage });
        if (!['pending', 'in_progress'].includes(processing.state)) throw new Error('Unknown X media processing state');
        const delay = Number(processing.check_after_secs);
        await this.wait(Number.isFinite(delay) ? Math.min(30_000, Math.max(1000, delay * 1000)) : 1000, undefined, { signal: uploadSignal });
        ({ data: final } = await this.request(url({ command: 'STATUS', media_id: id }), { signal: uploadSignal }));
      }
    } catch (error) {
      signal?.throwIfAborted();
      // Caption service/ambiguous API errors retain captions for a reconciled retry.
      const transient = [408, 429].includes(error.status) || error.status >= 500 || !error.status;
      if (error.stage !== 'session' && !error.apiErrors && (stage !== 'captions' || (!transient || error.stage === 'captions'))) error.stage = stage;
      throw error;
    }
  }

  uploadVideo(file, signal) { return this.upload({ file, type: 'video/mp4', category: 'amplify_video', stage: 'video' }, signal); }
  uploadImage(image, signal) { return this.upload({ data: Buffer.from(image.data, 'base64'), type: image.mime, category: image.mime === 'image/gif' ? 'tweet_gif' : 'tweet_image', stage: 'image' }, signal); }
  async attachSubtitles(video, srt, signal) {
    const subtitles = await this.upload({ data: Buffer.from(srt), type: 'text/plain', category: 'subtitles', stage: 'captions' }, signal);
    try {
      await this.request('https://x.com/i/api/1.1/media/subtitles/create.json', { method: 'POST', json: { media_id: video.id, media_category: video.category, subtitle_info: { subtitles: [{ media_id: subtitles.id, language_code: 'nl', display_name: 'Nederlands' }] } }, signal });
    } catch (error) {
      signal?.throwIfAborted();
      if (error.stage !== 'session' && !error.apiErrors && error.status >= 400 && error.status < 500 && ![408, 429].includes(error.status)) error.stage = 'captions';
      throw error;
    }
  }
  async createPost(text, media, signal) {
    const body = await this.graphql('CreateTweet', { tweet_text: text, media: { media_entities: media ? [{ media_id: media.id, tagged_users: [] }] : [], possibly_sensitive: false }, semantic_annotation_ids: [], disallowed_reply_options: null }, signal);
    return parseCreateTweetResponse(body, this.username);
  }
  async deletePost(id, signal) {
    if (!/^\d{1,25}$/.test(id)) throw new Error('Invalid post ID');
    await this.ensureSession(signal);
    parseDeleteTweetResponse(await this.graphql('DeleteTweet', { tweet_id: id }, signal));
  }
}
