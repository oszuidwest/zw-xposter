// Run explicitly (or through container/test.sh); the unit suite needs no browser.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtempDisposable } from 'node:fs/promises';
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
      if (event.key === 'Enter') document.body.innerHTML = '<h1>We hebben je een beveiligingscode gestuurd</h1><input name="text">';
    });
  });
</script>`;

test('a login code screen stops password logins until the session is back', { timeout: 300_000 }, async (t) => {
  const cleanup = new AsyncDisposableStack();
  t.after(() => cleanup.disposeAsync());
  process.env.DATA_DIR = cleanup.use(await mkdtempDisposable(path.join(os.tmpdir(), 'poster-login-'))).path;
  process.env.X_USERNAME = 'fixture_account';
  process.env.X_AUTH_TOKEN = '';
  process.env.X_PASSWORD = 'offline-test';
  process.env.HEADLESS = 'true';
  process.env.POSTER_PACING = '0';

  const poster = await import('./server.mjs');
  const server = poster.server;
  cleanup.defer(async () => {
    if (server.listening) await server[Symbol.asyncDispose]();
  });
  const context = cleanup.use(await poster.launch());
  let loggedIn = false;
  let logins = 0;
  await context.route('**/*', (route) => {
    const url = new URL(route.request().url());
    if (url.hostname !== 'x.com') return route.abort();
    if (url.pathname === '/home') {
      const body = loggedIn ? '<div data-testid="SideNav_AccountSwitcher_Button">fixture_account</div>' : '<a href="/login">Inloggen</a>';
      return route.fulfill({ contentType: 'text/html', body });
    }
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

  assert.match((await recent()).error, /^X asks for a code after the password/);
  assert.equal(logins, 1);
  assert.equal((await ready()).loginCodeRequested, true);

  // Past the ordinary 30-minute login backoff, the poster still does not log in again.
  t.mock.timers.enable({ apis: ['Date'], now: Date.now() + 31 * 60_000 });
  assert.match((await recent()).error, /^X asks for a code after the password/);
  t.mock.timers.reset();
  assert.equal(logins, 1);

  // A session that comes back clears the stop.
  loggedIn = true;
  await recent();
  assert.equal((await ready()).loginCodeRequested, false);
  assert.equal(logins, 1);
});
