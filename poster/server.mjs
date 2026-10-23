// Posts to X through persistent, logged-in Chromium.
//
// API:
//   GET /health -> 200 once Chromium has launched and HTTP is listening
//   GET /ready -> 200 after a recent successful session check
//   GET /recent?hours=48 -> own posts, newest first; 500 if the full window is unread
//   POST /post -> publish or dry-run; errors include whether the post was clicked
//   POST /post-video -> binary MP4, with base64 UTF-8 text in X-Post-Text
//   POST /reply -> reply to an own post or dry-run: { text, inReplyTo: status URL, dryRun }

import http from 'node:http';
import fs from 'node:fs/promises';
import path from 'node:path';
import { setInterval } from 'node:timers';
import { setTimeout as sleep } from 'node:timers/promises';
import { chromium } from 'playwright-core';
import xUI from './x-ui.json' with { type: 'json' };
import { pruneScreenshots } from './debug.mjs';
import { ANY_ATTACHMENT, ATTACHMENT, CAPTION_ATTACHED, MAX_VIDEO_BYTES, receiveVideo, uploadImage, uploadSubtitles, uploadVideo, videoPostText, waitForPostResponse } from './media.mjs';
import { prepareSubtitles } from './subtitles.mjs';
import {
  sessionCheckDelay,
  sessionHealth,
} from './health.mjs';
import {
  createTimelineCollector,
  isFirstUserTweetsPage,
  parseCreateTweetResponse,
  postErrorResponse,
  profileTab,
} from './timeline.mjs';

const PORT = Number(process.env.PORT || 8081);
const DATA_DIR = process.env.DATA_DIR || '/data';
const USERNAME = process.env.X_USERNAME;
const PASSWORD = process.env.X_PASSWORD;
const AUTH_TOKEN = process.env.X_AUTH_TOKEN || '';
// X's login challenge accepts an email address or phone number.
const VERIFICATION = process.env.X_VERIFICATION || '';
// Headed Chromium uses the container's virtual display.
const HEADLESS = process.env.HEADLESS === 'true';
const LANGUAGES = (process.env.BROWSER_LANGUAGES || 'nl-NL,nl,en-US,en').split(',');
const [WINDOW_WIDTH, WINDOW_HEIGHT] = (process.env.WINDOW_SIZE || '1440x960').split('x').map(Number);

if (import.meta.main && (!USERNAME || !(PASSWORD || AUTH_TOKEN))) {
  console.error('X_USERNAME and X_PASSWORD or X_AUTH_TOKEN are required');
  process.exit(1);
}

const PROFILE_DIR = path.join(DATA_DIR, 'profile');
const DEBUG_DIR = path.join(DATA_DIR, 'debug');
const DEBUG_RETENTION_MS = 14 * 24 * 3600_000;
const DEBUG_PRUNE_INTERVAL_MS = 24 * 3600_000;
const LOGIN_RETRY_MS = 30 * 60_000;
// Must match poster.MaxLookbackHours in internal/poster/client.go.
const MAX_RECENT_HOURS = 336;
// Reserve response headroom within the Go client's 5/30-minute timeouts;
// video receipt consumes up to five minutes before this budget starts.
const POST_TIMEOUT_MS = 4 * 60_000;
const VIDEO_POST_TIMEOUT_MS = 24 * 60_000;
const COOKIE_REFUSAL = new RegExp(xUI.cookieRefusalPattern, 'i');
const LOGIN_ERROR = new RegExp(xUI.loginErrorPattern, 'i');
const NOTICE_ACKNOWLEDGE = new RegExp(xUI.noticeAcknowledgePattern, 'i');
const MEDIA_UPLOAD_FAILED = new RegExp(xUI.mediaUploadFailedPattern, 'i');
const CARD_PREVIEW_REMOVE = new RegExp(xUI.cardPreviewRemovePattern, 'i');
const STATUS_URL = /^https:\/\/x\.com\/(\w{1,15})\/status\/(\d+)$/;

let context;
let tab;
let loggedIn = false;
let sessionCheckedAt = 0;
let loginFailedAt = 0;
let shuttingDown = false;

// Serialize browser work so session checks and posts cannot navigate over each other.
let queue = Promise.resolve();
function exclusive(fn) {
  const run = queue.then(fn);
  // Release settled values and let the next task run after failures.
  queue = run.then(() => {}, () => {});
  return run;
}

function log(...args) {
  console.log(new Date().toISOString(), ...args);
}

function random(min, max) {
  return min + Math.random() * (max - min);
}

// Only the offline browser test switches human pacing off; any other value keeps it.
const PACING = process.env.POSTER_PACING === '0' ? 0 : 1;

function pause(minMs, maxMs) {
  return sleep(random(minMs, maxMs) * PACING);
}

function settle(minMs, maxMs) {
  return sleep(random(minMs, maxMs));
}

// Keep Accept-Language and navigator.languages aligned through Chromium's profile.
async function writeLanguagePreference() {
  const file = path.join(PROFILE_DIR, 'Default', 'Preferences');
  let prefs = {};
  try {
    prefs = JSON.parse(await fs.readFile(file, 'utf8'));
  } catch {
    // Rebuild missing or unreadable preferences with only the language settings.
  }
  prefs.intl = { ...prefs.intl, accept_languages: LANGUAGES.join(','), selected_languages: LANGUAGES.join(',') };
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, JSON.stringify(prefs));
}

function pruneDebug() {
  return pruneScreenshots(DEBUG_DIR, { retentionMs: DEBUG_RETENTION_MS })
    .catch((err) => log('screenshot cleanup failed:', err.message));
}

export async function launch() {
  await fs.mkdir(DEBUG_DIR, { recursive: true });
  await pruneDebug();
  await writeLanguagePreference();

  context = await chromium.launchPersistentContext(PROFILE_DIR, {
    env: { ...process.env, ELEVENLABS_API_KEY: undefined },
    // Use full Chromium with its native user agent, client hints and platform.
    channel: 'chromium',
    headless: HEADLESS,
    // Let the desktop window determine the viewport dimensions.
    viewport: null,
    // Playwright's default flags announce automation (navigator.webdriver, infobar).
    ignoreDefaultArgs: ['--enable-automation'],
    args: [
      '--disable-blink-features=AutomationControlled',
      `--lang=${LANGUAGES[0]}`,
      `--window-size=${WINDOW_WIDTH},${WINDOW_HEIGHT}`,
      '--window-position=0,0',
      '--no-first-run',
      '--no-default-browser-check',
      // Render WebGL through Mesa on the virtual display.
      ...(HEADLESS ? [] : ['--use-angle=gl', '--ignore-gpu-blocklist']),
    ],
  });
  return context;
}

async function getTab() {
  if (tab && !tab.isClosed()) return tab;
  tab = context.pages().find((p) => !p.isClosed()) || (await context.newPage());
  for (const extra of context.pages()) {
    if (extra !== tab) await extra.close().catch(() => {});
  }
  tab.setDefaultTimeout(30_000);
  return tab;
}

async function screenshot(page, name) {
  const file = path.join(DEBUG_DIR, `${new Date().toISOString().replace(/[:.]/g, '-')}-${name}.png`);
  await page.screenshot({ path: file }).catch(() => {});
  return file;
}

// Retain each page's last click target for the next mouse path.
const mouse = new WeakMap();

// onPress marks the release attempt that can publish, even if confirmation fails.
async function humanClick(page, locator, { throwIfCancelled = () => {}, onPress = () => {} } = {}) {
  await locator.scrollIntoViewIfNeeded();
  const box = await locator.boundingBox();
  if (!box) throw new Error('element has no bounding box');

  const to = {
    x: box.x + box.width * random(0.3, 0.7),
    y: box.y + box.height * random(0.3, 0.7),
  };
  // Raw mouse clicks bypass locator actionability checks; reject covering elements.
  const covering = await locator.evaluate((el, { x, y }) => {
    const top = document.elementFromPoint(x, y);
    if (!top || el.contains(top)) return null;
    return `${top.tagName.toLowerCase()} "${(top.innerText || top.getAttribute('aria-label') || '').trim().slice(0, 60)}"`;
  }, to);
  if (covering) throw new Error(`element is covered by ${covering}`);
  const from = mouse.get(page) || { x: random(100, 600), y: random(100, 500) };
  const bend = { x: random(-80, 80), y: random(-60, 60) };
  const steps = PACING ? Math.round(random(18, 35)) : 1;
  for (let i = 1; i <= steps; i++) {
    const t = i / steps;
    // Ease in-out, plus a bend that is largest halfway.
    const e = t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2;
    const arc = Math.sin(Math.PI * t);
    await page.mouse.move(from.x + (to.x - from.x) * e + bend.x * arc, from.y + (to.y - from.y) * e + bend.y * arc);
    await pause(6, 18);
  }
  mouse.set(page, to);

  await pause(80, 250);
  await throwIfCancelled();
  await page.mouse.down();
  await pause(40, 120);
  try {
    await throwIfCancelled();
  } catch (err) {
    // Release away from the element so a cancel during mouse-down cannot click it.
    await page.mouse.move(0, 0).catch(() => {});
    await page.mouse.up().catch(() => {});
    throw err;
  }
  onPress();
  await page.mouse.up();
}

async function humanType(page, text) {
  for (const char of text) {
    await page.keyboard.type(char);
    if (char === ' ') await pause(90, 260);
    else if (/[.,:;!?’'"]/.test(char)) await pause(120, 320);
    else await pause(35, 110);
    if (Math.random() < 0.02) await pause(400, 1100);
  }
}

async function dismissOverlay(page, button, message) {
  if (!(await button.isVisible().catch(() => false))) return;
  await pause(700, 1800);
  await humanClick(page, button);
  await button.waitFor({ state: 'hidden', timeout: 10_000 }).catch(() => {});
  log(message);
}

// X's one-time video notice can cover the caption and Post buttons.
function acknowledgeNotice(page) {
  return dismissOverlay(page,
    page.getByRole('dialog').getByRole('button', { name: NOTICE_ACKNOWLEDGE }),
    'acknowledged an X notice');
}

async function isLoggedIn(page) {
  await page.goto('https://x.com/home', { waitUntil: 'domcontentloaded' });
  const nav = page.locator('[data-testid="SideNav_AccountSwitcher_Button"]');
  const login = page.locator('input[name="username_or_email"], a[href="/login"], a[href="/i/flow/login"]');
  await nav.or(login).first().waitFor({ timeout: 20_000 }).catch(() => {});
  // The banner renders a moment after the page.
  await settle(1000, 2000);
  await dismissOverlay(page, page.getByRole('button', { name: COOKIE_REFUSAL }), 'refused non-essential cookies');
  return nav.isVisible();
}

async function checkSession(page) {
  let active = false;
  try {
    active = await isLoggedIn(page);
  } finally {
    loggedIn = active;
    sessionCheckedAt = Date.now();
  }
  if (active) loginFailedAt = 0;
  return active;
}

async function pageMessage(page) {
  const text = await page.locator('body').innerText().catch(() => '');
  const lines = text.split('\n').filter((line) => LOGIN_ERROR.test(line));
  return [...new Set(lines)].join(' / ') || 'no error message on page';
}

async function login(page) {
  log('logging in as', USERNAME);
  await page.goto('https://x.com/i/flow/login', { waitUntil: 'domcontentloaded' });

  const user = page.locator('input[name="username_or_email"]');
  await user.waitFor();
  await pause(800, 2000);
  await humanClick(page, user);
  await humanType(page, USERNAME);
  await pause(300, 900);
  await page.keyboard.press('Enter');

  const password = page.locator('input[name="password"]:not([inert])');
  // X sometimes asks for the email address or phone number before the password.
  const challenge = page.locator('input[data-testid="ocfEnterTextTextInput"], input[name="text"]');
  try {
    await password.or(challenge).first().waitFor();
  } catch {
    throw new Error(`login stuck after username: ${await pageMessage(page)} (screenshot: ${await screenshot(page, 'login')})`);
  }
  if (!(await password.isVisible())) {
    if (!VERIFICATION) {
      throw new Error(`X asks for verification; set X_VERIFICATION (screenshot: ${await screenshot(page, 'verification')})`);
    }
    await pause(600, 1500);
    await humanClick(page, challenge.first());
    await humanType(page, VERIFICATION);
    await page.keyboard.press('Enter');
    await password.waitFor();
  }

  await pause(600, 1500);
  await humanClick(page, password);
  await humanType(page, PASSWORD);
  await pause(300, 900);
  await page.keyboard.press('Enter');

  try {
    await page.waitForURL(/x\.com\/home/, { timeout: 30_000 });
  } catch {
    throw new Error(`login did not reach /home: ${await pageMessage(page)} (screenshot: ${await screenshot(page, 'login')})`);
  }
  log('logged in');
}

async function ensureLoggedIn(page) {
  if (await checkSession(page)) {
    return;
  }
  if (AUTH_TOKEN) {
    log('restoring session from X_AUTH_TOKEN');
    await context.addCookies([{
      name: 'auth_token',
      value: AUTH_TOKEN,
      domain: '.x.com',
      path: '/',
      expires: Math.floor(Date.now() / 1000) + 365 * 24 * 3600,
      httpOnly: true,
      secure: true,
      sameSite: 'None',
    }]);
    if (await checkSession(page)) {
      return;
    }
    log('X_AUTH_TOKEN is not (or no longer) valid');
  }
  if (!PASSWORD) throw new Error('logged out and no X_PASSWORD to log in with');
  const wait = loginBlockedUntil() - Date.now();
  if (wait > 0) throw new Error(`logged out; previous login failed, next attempt in ${Math.ceil(wait / 60_000)} min`);
  try {
    await login(page);
    if (!(await checkSession(page))) throw new Error(`still logged out after login (screenshot: ${await screenshot(page, 'session')})`);
  } catch (err) {
    loginFailedAt = Date.now();
    throw err;
  }
}

function loginBlockedUntil() {
  return loginFailedAt > 0 ? loginFailedAt + LOGIN_RETRY_MS : 0;
}

async function runSessionChecks() {
  while (!shuttingDown) {
    await sleep(sessionCheckDelay());
    if (shuttingDown) return;
    await exclusive(async () => {
      // Checked in the queue, as a login may have failed while this waited.
      if (loginBlockedUntil() > Date.now()) {
        log('background session check skipped during login backoff');
        return;
      }
      try {
        await ensureLoggedIn(await getTab());
        log('background session check succeeded');
      } catch (err) {
        log('background session check failed:', err.message);
      }
    });
  }
}

// Returns the post ID of a status URL on this account, or null.
function ownStatusID(url) {
  const match = typeof url === 'string' && STATUS_URL.exec(url);
  return match && match[1].toLowerCase() === USERNAME?.toLowerCase() ? match[2] : null;
}

// Fallback requires verified cleanup of failed attachments and dialogs.
async function resetComposer(page, throwIfGone) {
  throwIfGone();
  await page.goto('https://x.com/home', { waitUntil: 'domcontentloaded' });
  await page.locator('[data-testid="SideNav_AccountSwitcher_Button"]').waitFor();
  if (await page.locator(`[role="dialog"]:visible, ${ANY_ATTACHMENT}`).count()) {
    throw new Error('composer recovery could not be verified');
  }
  throwIfGone();
}

function composer(page) {
  const dialog = page.locator('[role="dialog"][aria-modal="true"]').filter({ has: page.locator('[data-testid="tweetTextarea_0"]') });
  return { dialog, box: dialog.locator('[data-testid="tweetTextarea_0"]').first() };
}

// Close the open composer without saving a draft.
async function discardComposer(page, dialog) {
  await page.keyboard.press('Escape');
  // "Save post?" sheet: confirm saves a draft, cancel discards.
  await humanClick(page, page.locator('[data-testid="confirmationSheetCancel"]'));
  await dialog.waitFor({ state: 'detached', timeout: 15_000 });
}

// Click Post and return X's confirmation; clicked() marks the press that can publish.
async function publish(page, dialog, signal, { throwIfGone, clicked, inReplyTo }) {
  const release = new AbortController();
  const response = waitForPostResponse(page, { signal: AbortSignal.any([signal, release.signal]) });
  // A failed click can leave this rejection unawaited.
  response.catch(() => {});
  try {
    await humanClick(page, dialog.locator('[data-testid="tweetButton"]'), { throwIfCancelled: throwIfGone, onPress: clicked });
    const body = await (await response).json();
    const result = parseCreateTweetResponse(body, USERNAME, { inReplyTo });
    await dialog.waitFor({ state: 'detached', timeout: 15_000 }).catch(() => {});
    return result;
  } finally {
    release.abort();
  }
}

// Describe a failed publication after verifying a clean composer.
// A failed recovery is a service failure, never a media failure.
async function publicationError(page, cause, { clicked, recover, ...details }) {
  // AbortSignal throws a DOMException with a read-only message.
  const err = Object.assign(new Error(cause.message, { cause }), { stage: cause.stage, clicked, ...details });
  if (page && clicked) {
    const banner = page.getByText(MEDIA_UPLOAD_FAILED);
    if (await banner.isVisible().catch(() => false)) {
      const message = await banner.innerText({ timeout: 1000 }).catch(() => '');
      if (message) err.message += ` (X: ${message})`;
    }
  }
  if (page) err.message += ` (screenshot: ${await screenshot(page, 'error')})`;
  if (clicked) err.message += ' (after clicking post; it may be on X)';
  try {
    await recover();
  } catch (recovery) {
    err.stage = 'service';
    err.message += ` (recovery failed: ${recovery.message})`;
  }
  return err;
}

// Abandon disconnected requests before the click; no client remains to record the outcome.
// videoFile is server-owned; request payloads must never supply local paths.
async function createPost({ text, image, dryRun, skipCaptions }, signal, videoFile) {
  const throwIfGone = () => signal.throwIfAborted();
  // The request may have waited in the queue behind other browser work.
  throwIfGone();
  let { srt, ...caption } = videoFile ? await prepareSubtitles(videoFile, { signal, skip: skipCaptions, dryRun }) : {};
  if (caption.fallbackReason) log('video without captions:', caption.fallbackReason);
  throwIfGone();
  let page;
  let clicked = false;
  let cancelledPage;
  const closeOnCancel = () => { cancelledPage = page.close().catch(() => {}); };
  const mediaOperation = async (stage, operation) => {
    throwIfGone();
    try {
      await operation();
    } catch (err) {
      throwIfGone();
      // Closed browsers and explicitly identified session failures are not media failures.
      if (!page.isClosed() && !err.stage) err.stage = stage;
      throw err;
    }
    throwIfGone();
  };
  try {
    page = await getTab();
    throwIfGone();
    signal.addEventListener('abort', closeOnCancel, { once: true });
    await ensureLoggedIn(page);
    for (let attempt = 0; attempt < 2; attempt++) {
      throwIfGone();
      await pause(2000, 5000);
      if (Math.random() < 0.6) {
        await page.mouse.wheel(0, random(200, 700));
        await pause(1000, 3000);
      }

      await humanClick(page, page.locator('[data-testid="SideNav_NewTweet_Button"]'));
      const { dialog, box } = composer(page);
      await box.waitFor();
      if (await dialog.locator(ANY_ATTACHMENT).count()) {
        throw new Error('new composer contains stale media');
      }
      await pause(500, 1500);
      await humanClick(page, box);
      await humanType(page, text);

      if (videoFile) {
        await mediaOperation('video', () => uploadVideo(page, dialog, videoFile, { throwIfCancelled: throwIfGone }));
        await acknowledgeNotice(page);
        if (srt) {
          try {
            await mediaOperation('captions', () => uploadSubtitles(page, dialog, srt, { throwIfCancelled: throwIfGone }));
          } catch (err) {
            if (err.stage !== 'captions') throw err;
            caption.captions = 'none';
            caption.fallbackReason = `caption attachment: ${err.message}`;
            log('video without captions:', caption.fallbackReason);
            srt = undefined;
            await resetComposer(page, throwIfGone);
            continue; // One clean re-upload; no repeated transcription or caption attempt.
          }
        }
      } else if (image) {
        await pause(800, 2000);
        await mediaOperation('image', () => uploadImage(page, dialog, image, { throwIfCancelled: throwIfGone }));
      }

      await dialog.locator('[data-testid="tweetButton"]:not([aria-disabled="true"]):not([disabled])').waitFor({ timeout: 60_000 });
      // Allow delayed overlays and attachment changes to appear before the final checks.
      await settle(1500, 4000);
      await acknowledgeNotice(page);
      throwIfGone();
      const videos = await dialog.locator(ATTACHMENT.video).count();
      const images = await dialog.locator(ATTACHMENT.image).count();
      if (videos !== (videoFile ? 1 : 0) || images !== (image ? 1 : 0)) {
        throw new Error('composer media state changed before publication');
      }
      if (videoFile) {
        const attached = await dialog.getByText(CAPTION_ATTACHED).isVisible();
        if (attached !== (caption.captions === 'attached')) throw new Error('composer caption state changed before publication');
      }

      if (dryRun) {
        const file = await screenshot(page, 'dry-run');
        await discardComposer(page, dialog);
        return { dryRun: true, screenshot: file };
      }

      try {
        const result = await publish(page, dialog, signal, { throwIfGone, clicked: () => { clicked = true; } });
        return { ...result, ...caption };
      } catch (err) {
        if (err.stage === 'captions') {
          caption.captions = 'none';
          caption.fallbackReason = `caption upload: ${err.message}`;
          // Keep clicked=true so the retry checks X before publishing again.
          log('video without captions on retry:', caption.fallbackReason);
        }
        throw err;
      }
    }
    throw new Error('composer recovery exhausted');
  } catch (cause) {
    throw await publicationError(page, cause, { clicked, recover: () => resetComposer(page, throwIfGone), ...caption });
  } finally {
    signal.removeEventListener('abort', closeOnCancel);
    // Do not release the browser queue while cancellation is still closing the tab.
    await cancelledPage;
  }
}

// Opens an own post and replies to it; the response must confirm that parent.
async function createReply({ text, inReplyTo, dryRun }, signal) {
  const throwIfGone = () => signal.throwIfAborted();
  throwIfGone();
  const id = ownStatusID(inReplyTo);
  let page;
  let clicked = false;
  let cancelledPage;
  const closeOnCancel = () => { cancelledPage = page.close().catch(() => {}); };
  try {
    page = await getTab();
    throwIfGone();
    signal.addEventListener('abort', closeOnCancel, { once: true });
    await ensureLoggedIn(page);
    await pause(2000, 5000);
    throwIfGone();
    await page.goto(`https://x.com/${USERNAME}/status/${id}`, { waitUntil: 'domcontentloaded' });
    // Its timestamp links to the post itself; replies below link to their own IDs.
    const post = page.locator('article[data-testid="tweet"]').filter({ has: page.locator(`a[href$="/status/${id}" i] time`) }).first();
    await post.waitFor();
    await pause(1500, 4000);
    throwIfGone();

    await humanClick(page, post.locator('[data-testid="reply"]'));
    const { dialog, box } = composer(page);
    await box.waitFor();
    if (await dialog.locator(ANY_ATTACHMENT).count()) {
      throw new Error('new reply composer contains stale media');
    }
    await pause(500, 1500);
    await humanClick(page, box);
    await humanType(page, text);
    // The headline post already shows the article image; remove X's preview card, which loads after the link.
    const card = dialog.locator('[data-testid="card.wrapper"]');
    if (await card.waitFor({ timeout: 10_000 }).then(() => true, () => false)) {
      await pause(500, 1500);
      await humanClick(page, dialog.getByRole('button', { name: CARD_PREVIEW_REMOVE }));
      // Refocus so Escape can discard the composer during a dry run.
      await pause(300, 900);
      await humanClick(page, box);
    }
    await dialog.locator('[data-testid="tweetButton"]:not([aria-disabled="true"]):not([disabled])').waitFor({ timeout: 60_000 });
    // Leave time for a late or refetched preview card before the final checks.
    await settle(1500, 4000);
    await acknowledgeNotice(page);
    // Also checked while pressing Post, as the card can return on the way there.
    const throwIfUnsafe = async () => {
      throwIfGone();
      if (await card.count()) throw new Error('link preview card still present before publication');
    };
    await throwIfUnsafe();

    if (dryRun) {
      const file = await screenshot(page, 'dry-run-reply');
      await discardComposer(page, dialog);
      return { dryRun: true, screenshot: file };
    }
    return await publish(page, dialog, signal, { throwIfGone: throwIfUnsafe, clicked: () => { clicked = true; }, inReplyTo: id });
  } catch (cause) {
    throw await publicationError(page, cause, { clicked, recover: () => resetComposer(page, throwIfGone) });
  } finally {
    signal.removeEventListener('abort', closeOnCancel);
    // Do not release the browser queue while cancellation is still closing the tab.
    await cancelledPage;
  }
}

async function recentPosts(hours) {
  const page = await getTab();
  await ensureLoggedIn(page);
  const twid = (await context.cookies('https://x.com')).find((c) => c.name === 'twid')?.value || '';
  const ownId = decodeURIComponent(twid).replace(/^u=/, '');
  const cutoff = Date.now() - hours * 3600_000;

  // Headline posts and their link replies are on separate tabs; both must be complete.
  const timelines = {
    posts: createTimelineCollector({ ownId, username: USERNAME, cutoff }),
    replies: createTimelineCollector({ ownId, username: USERNAME, cutoff }),
  };
  let pending = 0;
  const inFlight = new Set();
  let requestsStarted = 0;
  const onRequest = (request) => {
    if (!profileTab(request.url())) return;
    inFlight.add(request);
    requestsStarted++;
  };
  // A failed request's missing page already leaves the tab incomplete.
  const onRequestDone = (request) => inFlight.delete(request);
  const onResponse = (r) => {
    const timeline = timelines[profileTab(r.url())];
    if (!timeline) return;
    if (!r.ok()) {
      timeline.fail(`UserTweets returned HTTP ${r.status()}`);
      return;
    }
    const firstPage = isFirstUserTweetsPage(r.url());
    pending++;
    r.json().then((body) => timeline.add(body, { firstPage })).catch((err) => timeline.fail(err.message)).finally(() => pending--);
  };
  const finishRequests = async (timeline) => {
    if (timeline.failed()) return;
    const deadline = Date.now() + 30_000;
    while (inFlight.size || pending) {
      if (Date.now() >= deadline) {
        timeline.fail(pending
          ? 'a UserTweets response body did not arrive within 30 seconds'
          : 'a UserTweets request did not finish within 30 seconds');
        return;
      }
      await sleep(100);
    }
  };
  const readTab = async (timeline) => {
    const done = () => timeline.complete() || timeline.failed();
    // An empty tab never shows an article, so a complete or failed timeline also ends the wait.
    const deadline = Date.now() + 30_000;
    while (!done() && !(await page.locator('article').first().isVisible())) {
      if (Date.now() >= deadline) timeline.fail(`no visible posts within 30 seconds: ${timeline.incompleteReason()}`);
      else await sleep(100);
    }
    await settle(1500, 3000);
    for (let i = 0; i < 8; i++) {
      await finishRequests(timeline);
      if (done()) break;
      const started = requestsStarted;
      await page.mouse.wheel(0, random(1500, 2500));
      await pause(1500, 3000);
      const settleDeadline = Date.now() + 1000;
      while (requestsStarted === started && !done() && Date.now() < settleDeadline) {
        await sleep(100);
      }
    }
    await finishRequests(timeline);
  };

  page.on('request', onRequest);
  page.on('requestfinished', onRequestDone);
  page.on('requestfailed', onRequestDone);
  page.on('response', onResponse);
  try {
    await pause(1000, 2500);
    await humanClick(page, page.locator('[data-testid="AppTabBar_Profile_Link"]'));
    await page.waitForURL(new RegExp(`x\\.com/${USERNAME}$`, 'i'));
    await readTab(timelines.posts);
    if (timelines.posts.complete()) {
      await pause(1000, 2500);
      await humanClick(page, page.locator('a[role="tab"][href$="/with_replies" i]'));
      await page.waitForURL(new RegExp(`x\\.com/${USERNAME}/with_replies$`, 'i'));
      await readTab(timelines.replies);
    }
  } finally {
    page.off('request', onRequest);
    page.off('requestfinished', onRequestDone);
    page.off('requestfailed', onRequestDone);
    page.off('response', onResponse);
  }

  // Fail the request: partial history cannot rule out an existing post.
  const reason = (timelines.posts.incompleteReason() && `Posts tab: ${timelines.posts.incompleteReason()}`)
    || (timelines.replies.incompleteReason() && `Replies tab: ${timelines.replies.incompleteReason()}`);
  if (reason) throw new Error(`recent posts incomplete: ${reason} (screenshot: ${await screenshot(page, 'recent')})`);
  const posts = new Map([...timelines.posts.result().posts, ...timelines.replies.result().posts].map((post) => [post.id, post]));
  return { posts: [...posts.values()].sort((a, b) => b.createdAt.localeCompare(a.createdAt)), complete: true };
}

function send(res, status, body) {
  res.writeHead(status, { 'content-type': 'application/json' });
  res.end(JSON.stringify(body));
}

async function postAndSend(res, post, videoFile, create = createPost) {
  const kind = (videoFile && 'video') || (create === createReply && 'reply');
  log(kind ? `posting ${kind}:` : 'posting:', post.text.split('\n')[0].slice(0, 100));
  const controller = new AbortController();
  if (res.destroyed) controller.abort();
  else res.once('close', () => controller.abort());
  // Transcribe inside the queue so posts keep their request order.
  // The budget covers queueing, transcription, at most two uploads and confirmation.
  const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(videoFile ? VIDEO_POST_TIMEOUT_MS : POST_TIMEOUT_MS)]);
  const result = await exclusive(() => create(post, signal, videoFile));
  log(result.dryRun ? 'dry run done' : 'posted', result.url || result.screenshot);
  send(res, 200, result);
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

export const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://localhost');
    if (req.method === 'GET' && url.pathname === '/health') {
      return send(res, 200, {});
    }
    if (req.method === 'GET' && url.pathname === '/ready') {
      const status = sessionHealth({ loggedIn, checkedAt: sessionCheckedAt, blockedUntil: loginBlockedUntil() });
      return send(res, status.ready ? 200 : 503, status.body);
    }
    if (req.method === 'GET' && url.pathname === '/recent') {
      const hours = Number(url.searchParams.get('hours') || 48);
      if (!(hours > 0 && hours <= MAX_RECENT_HOURS)) return send(res, 400, { error: `hours must be between 0 and ${MAX_RECENT_HOURS}` });
      const result = await exclusive(() => recentPosts(hours));
      log(`read ${result.posts.length} posts from the last ${hours}h`);
      return send(res, 200, result);
    }
    if (req.method === 'POST' && url.pathname === '/post') {
      const payload = await readJSON(req);
      if (payload.videoFile) return send(res, 400, { error: 'use /post-video to upload a video' });
      if (typeof payload.text !== 'string' || !payload.text.trim()) {
        return send(res, 400, { error: 'text is required' });
      }
      if (payload.image && (!payload.image.mime || !payload.image.data)) {
        return send(res, 400, { error: 'image needs mime and data' });
      }
      return await postAndSend(res, payload);
    }
    if (req.method === 'POST' && url.pathname === '/reply') {
      const payload = await readJSON(req);
      if (typeof payload.text !== 'string' || !payload.text.trim()) {
        return send(res, 400, { error: 'text is required' });
      }
      if (!ownStatusID(payload.inReplyTo)) {
        return send(res, 400, { error: `inReplyTo must be a post URL of @${USERNAME}` });
      }
      return await postAndSend(res, { text: payload.text, inReplyTo: payload.inReplyTo, dryRun: payload.dryRun }, undefined, createReply);
    }
    if (req.method === 'POST' && url.pathname === '/post-video') {
      let text;
      try {
        if (req.headers['content-type'] !== 'video/mp4') throw new Error('video/mp4 is required');
        if (Number(req.headers['content-length']) > MAX_VIDEO_BYTES) throw new Error('video is too large');
        text = videoPostText(req.headers['x-post-text']);
        if (req.headers['x-post-captions'] && req.headers['x-post-captions'] !== 'none') throw new Error('X-Post-Captions must be none or absent');
      } catch (err) {
        return send(res, 400, postErrorResponse(err));
      }
      const video = await receiveVideo(req);
      try {
        return await postAndSend(res, { text, skipCaptions: req.headers['x-post-captions'] === 'none' }, video.file);
      } finally {
        await video.cleanup().catch((err) => log('video cleanup failed:', err.message));
      }
    }
    send(res, 404, { error: 'not found' });
  } catch (err) {
    log('error:', err.message);
    send(res, 500, postErrorResponse(err));
  }
});

if (import.meta.main) {
  await launch();
  context.on('close', () => {
    if (shuttingDown) return;
    log('browser context closed unexpectedly, exiting');
    process.exit(1);
  });
  setInterval(pruneDebug, DEBUG_PRUNE_INTERVAL_MS);
  // Serve passive health checks during login; browser work queues behind it.
  server.listen(PORT, '127.0.0.1', () => log(`poster listening on 127.0.0.1:${PORT}, headless: ${HEADLESS}`));
  exclusive(async () => {
    const page = await getTab();
    try {
      await ensureLoggedIn(page);
      log('session ready');
    } catch (err) {
      log('initial login failed:', err.message, `(screenshot: ${await screenshot(page, 'startup')})`);
    }
  });
  runSessionChecks().catch((err) => log('session checker stopped:', err.message));

  for (const signal of ['SIGINT', 'SIGTERM']) {
    process.on(signal, async () => {
      shuttingDown = true;
      server.close();
      await context.close().catch(() => {});
      process.exit(0);
    });
  }
}
