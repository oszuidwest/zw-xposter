# ADR 0001: Use browser automation as the X posting adapter

- Status: Accepted
- Date: 2026-10-03
- Owner: Raymon Mens ([@rmens](https://github.com/rmens))
- Review date: 2027-04-01

## Context

ZuidWest Update needs to publish new articles to X. The official X API and authorized providers are not financially viable for this use case. Browser automation with Playwright can perform the required work at acceptable cost, but it depends on an undocumented user interface and on an account that X can restrict or suspend.

Browser fingerprint changes do not remove that platform risk. Selector changes, login-flow changes, rate limits, account restrictions and policy enforcement can interrupt posting without notice.

## Decision

Keep the Playwright poster as a replaceable, inherently unreliable adapter behind a small HTTP boundary. The Go orchestrator owns durable workflow state, duplicate prevention, retry timing, operational status and recovery. A browser response alone is not the durable source of truth: uncertain outcomes are reconciled against a complete profile timeline before another post is allowed.

All browser work is serialized through one exclusive queue. A disconnected client stops a post before the final click. After the click, the poster finishes observing the outcome even if the client disconnects. Timeline checks use the same queue, so reconciliation cannot overtake an in-flight click.

The deployment is a singleton. One orchestrator owns a state file and one poster owns a persistent Chromium profile. Non-blocking locks in the data volume enforce this on one Docker host; this design does not provide distributed locking between hosts.

The default deployment packages both applications in one Ubuntu 26.04 image and container, running as the same non-root user with one data volume and communicating over localhost. Graph credentials are excluded from the browser's environment, but the shared user is not a security boundary. The launcher stops the container if either application exits. The HTTP boundary remains a code boundary, not a separate-container security boundary.

## Consequences

- Posting remains affordable without making the browser adapter authoritative.
- Delivery is best-effort: the platform risks described under Context are accepted.
- The orchestrator writes `posting` before requesting a post. Lost responses and interrupted work become `uncertain` and must be checked against X before retrying.
- Duplicate prevention fails closed: an incomplete timeline check prevents a post.
- Operators must monitor readiness, alerts and terminal workflow states, and must maintain the X session.
- A second instance must not share the data volume. Multi-host active/active deployment is unsupported.
- Normal container stop and restart operations affect both applications. Browser diagnostics with automatic publication disabled use a temporary dry-run container; manual poster requests still require their own `dryRun: true` flag.
- More anti-detection measures are not treated as a solution to the accepted platform risk.

This decision will be reconsidered on the review date, or earlier if X changes its API or provider pricing, browser access becomes untenable, or the account-risk profile materially changes.
