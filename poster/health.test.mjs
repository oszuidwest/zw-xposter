import assert from 'node:assert/strict';
import test from 'node:test';

import {
  sessionCheckDelay,
  sessionHealth,
} from './health.mjs';

const now = Date.parse('2026-10-03T12:00:00.000Z');

test('readiness requires a recent successful session check without login backoff', () => {
  const healthy = sessionHealth({
    loggedIn: true,
    checkedAt: now - 60_000,
    blockedUntil: 0,
    now,
  });
  assert.equal(healthy.ready, true);
  assert.deepEqual(healthy.body, {
    loggedIn: true,
    checkedAt: '2026-10-03T11:59:00.000Z',
    loginBlockedUntil: null,
    loginCodeRequested: false,
    sessionError: null,
  });

  for (const [overrides, loginBlockedUntil] of [
    [{ loggedIn: false }, null],
    [{ checkedAt: 0 }, null],
    [{ checkedAt: now - 70 * 60_000 - 1 }, null],
    [{ blockedUntil: now + 25 * 60_000 }, '2026-10-03T12:25:00.000Z'],
  ]) {
    const status = sessionHealth({
      loggedIn: true,
      checkedAt: now,
      blockedUntil: 0,
      now,
      ...overrides,
    });
    assert.equal(status.ready, false);
    assert.equal(status.body.loginBlockedUntil, loginBlockedUntil);
  }
});

test('readiness reports that X asked for a login code and why the session check failed', () => {
  const status = sessionHealth({
    loggedIn: false,
    checkedAt: now,
    blockedUntil: 0,
    loginCodeRequested: true,
    sessionError: 'session unclear: neither the account nor a login prompt',
    now,
  });
  assert.equal(status.body.loginCodeRequested, true);
  assert.equal(status.body.sessionError, 'session unclear: neither the account nor a login prompt');
});

test('session-check jitter stays between thirty and sixty minutes', () => {
  assert.equal(sessionCheckDelay(() => 0), 30 * 60_000);
  assert.equal(sessionCheckDelay(() => 1), 60 * 60_000);
  assert.equal(sessionCheckDelay(() => 0.5), 45 * 60_000);
});
