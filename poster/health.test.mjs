import assert from 'node:assert/strict';
import test from 'node:test';

import {
  sessionCheckDelay,
  sessionHealth,
} from './health.mjs';

const now = Date.parse('2026-10-03T12:00:00.000Z');

test('readiness requires a recent successful session check', () => {
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
  });

  for (const overrides of [
    { loggedIn: false },
    { checkedAt: 0 },
    { checkedAt: now - 70 * 60_000 - 1 },
  ]) {
    assert.equal(sessionHealth({
      loggedIn: true,
      checkedAt: now,
      blockedUntil: 0,
      now,
      ...overrides,
    }).ready, false);
  }
});

test('login backoff makes readiness fail and is exposed in the body', () => {
  const status = sessionHealth({
    loggedIn: true,
    checkedAt: now,
    blockedUntil: now + 25 * 60_000,
    now,
  });

  assert.equal(status.ready, false);
  assert.equal(status.body.loginBlockedUntil, '2026-10-03T12:25:00.000Z');
});

test('session-check jitter stays between thirty and sixty minutes', () => {
  assert.equal(sessionCheckDelay(() => 0), 30 * 60_000);
  assert.equal(sessionCheckDelay(() => 1), 60 * 60_000);
  assert.equal(sessionCheckDelay(() => 0.5), 45 * 60_000);
});
