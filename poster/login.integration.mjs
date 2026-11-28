// Run explicitly (or through container/test.sh); the unit suite needs no browser.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtempDisposable, readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

// X's login flow as seen in production on 2026-11-26: username, password, then an emailed code.
const loginPage = `<input name="username_or_email">
<script>
  document.querySelector('input').addEventListener('keydown', (event) => {
    if (event.key !== 'Enter') return;
    document.body.innerHTML = '<input name="password">';
    document.querySelector('input').addEventListener('keydown', (event) => {
      if (event.key === 'Enter') document.body.innerHTML = '<h1>We hebben je een beveiligingscode gestuurd</h1><input name="text" value="typed-code">';
    });
  });
</script>`;

test('the session check logs in again only after a clear logout; a login code screen spaces password logins six hours apart', { timeout: 300_000 }, async (t) => {
  const cleanup = new AsyncDisposableStack();
  t.after(() => cleanup.disposeAsync());
  process.env.DATA_DIR = cleanup.use(await mkdtempDisposable(path.join(os.tmpdir(), 'poster-login-'))).path;
  process.env.X_USERNAME = 'fixture_account';
  process.env.X_AUTH_TOKEN = 'offline-test';
  process.env.X_PASSWORD = 'offline-test';
  process.env.HEADLESS = 'true';
  process.env.POSTER_PACING = '0';
  process.env.POSTER_X_WAIT_MS = '1000';

  const poster = await import('./server.mjs');
  const server = poster.server;
  cleanup.defer(async () => {
    if (server.listening) await server[Symbol.asyncDispose]();
  });
  const context = cleanup.use(await poster.launch());
  // X's new web client (2026-11-27): almost no test IDs; the account button holds the avatar with the handle as alt text.
  const signedIn = '<button aria-label="Accountmenu"><img alt="@Fixture_Account" width="40" height="40"></button>';
  let home = signedIn;
  let tokenWorks = false;
  let logins = 0;
  const hasToken = async () => (await context.cookies()).some((cookie) => cookie.name === 'auth_token');
  await context.route('**/*', async (route) => {
    const url = new URL(route.request().url());
    if (url.hostname !== 'x.com') return route.abort();
    if (url.pathname === '/home') return route.fulfill({ contentType: 'text/html', body: tokenWorks && (await hasToken()) ? signedIn : home });
    if (url.pathname === '/fixture_account') return route.fulfill({ contentType: 'text/html', body: '' });
    if (url.pathname === '/i/flow/login') {
      logins++;
      return route.fulfill({ contentType: 'text/html', body: loginPage });
    }
    return route.abort();
  });

  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const base = `http://127.0.0.1:${server.address().port}`;
  const recent = async () => (await fetch(`${base}/recent?hours=1`)).json();
  const ready = async () => (await fetch(`${base}/ready`)).json();

  await recent();
  assert.equal((await ready()).loggedIn, true);
  // A page without a visible account button or login prompt touches neither token nor password,
  // even with hidden copies of both and the account's avatar elsewhere, as on a post.
  home = '<header hidden><button><img alt="@fixture_account"></button><a href="/login">Inloggen</a></header><main><img alt="@fixture_account" width="40" height="40"></main>';
  const unclear = (await recent()).error;
  assert.match(unclear, /^session unclear: neither the account nor a login prompt on https:\/\/x\.com\/home.*\(screenshot: /);
  assert.partialDeepStrictEqual(await ready(), { loggedIn: false, sessionError: unclear });
  // Contradicting signals are unclear too, and a page that stays unclear gets no new screenshot.
  home = '<button><img alt="@fixture_account"></button><a href="/login">Inloggen</a>';
  assert.match((await recent()).error, /^session unclear: both the account and a login prompt on [^(]*$/);
  assert.equal(await hasToken(), false);
  // Logged out, the new client redirects /home to a landing page with this link; the fixture serves it at /home.
  home = '<a href="/i/jf/onboarding/web?mode=login&amp;redirect_after_login=%2F">Inloggen</a>';
  // A clear logout restores X_AUTH_TOKEN before any password login.
  tokenWorks = true;
  await recent();
  assert.partialDeepStrictEqual(await ready(), { loggedIn: true, sessionError: null });
  tokenWorks = false;

  const start = Date.now();
  const code = /X asks for a code after the password/;
  const { error } = await recent();
  assert.match(error, code);
  // Each screenshot gets the page HTML next to it, headed by the page URL and without typed input values.
  const html = await readFile(error.match(/screenshot: (\S+)\.png/)[1] + '.html', 'utf8');
  assert.match(html, /^<!-- https:\/\/x\.com\/i\/flow\/login -->\n<html>/);
  assert.doesNotMatch(html, /typed-code/);
  assert.equal(logins, 1);
  const status = await ready();
  assert.equal(status.sessionError, error);
  assert.equal(status.loginCodeRequested, true);
  const until = Date.parse(status.loginBlockedUntil);
  assert.ok(until >= start + 6 * 3600_000);

  // Past the ordinary 30-minute backoff, the poster still waits.
  t.mock.timers.enable({ apis: ['Date'], now: start + 31 * 60_000 });
  // The backoff repeats the failed login's reason, and /ready keeps showing it.
  const blocked = (await recent()).error;
  assert.match(blocked, /; next attempt in 3\d\d min$/);
  assert.ok(blocked.startsWith(`logged out; last login failed: ${error}; `));
  assert.equal((await ready()).sessionError, blocked);
  assert.equal(logins, 1);
  // After six hours it tries once more, this time from the old client's logged-out page.
  home = '<a href="/login">Inloggen</a>';
  t.mock.timers.setTime(until + 60_000);
  assert.match((await recent()).error, code);
  t.mock.timers.reset();
  assert.equal(logins, 2);

  // A session that comes back during the new wait clears the flag.
  home = '<div data-testid="SideNav_AccountSwitcher_Button">fixture_account</div><a data-testid="AppTabBar_Profile_Link" href="/fixture_account">Profiel</a>';
  await recent();
  assert.equal((await ready()).loginCodeRequested, false);
  assert.equal(logins, 2);
});
