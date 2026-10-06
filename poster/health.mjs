// Pure session-health bookkeeping; health handlers never send requests to X.

const SESSION_CHECK_MIN_MS = 30 * 60_000;
const SESSION_CHECK_MAX_MS = 60 * 60_000;
// Allow a little scheduling delay beyond the longest background interval.
const SESSION_CHECK_MAX_AGE_MS = 70 * 60_000;

export function sessionCheckDelay(random = Math.random) {
  return SESSION_CHECK_MIN_MS + random() * (SESSION_CHECK_MAX_MS - SESSION_CHECK_MIN_MS);
}

// blockedUntil is the end of the login backoff, or 0 without one.
export function sessionHealth({ loggedIn, checkedAt, blockedUntil, now = Date.now() }) {
  const loginBlocked = blockedUntil > now;
  const recent = checkedAt > 0 && now - checkedAt <= SESSION_CHECK_MAX_AGE_MS;

  return {
    ready: Boolean(loggedIn && recent && !loginBlocked),
    body: {
      loggedIn: Boolean(loggedIn),
      checkedAt: checkedAt > 0 ? new Date(checkedAt).toISOString() : null,
      loginBlockedUntil: loginBlocked ? new Date(blockedUntil).toISOString() : null,
    },
  };
}
