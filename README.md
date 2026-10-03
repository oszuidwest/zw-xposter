# zw-xposter

Posts new articles from the [ZuidWest Update](https://www.zuidwestupdate.nl/) RSS feed to [@zwupdate](https://x.com/zwupdate), with a link and share image. Replaces dlvr.it.

A Go process polls the feed; a Node.js/Playwright process posts through a logged-in Chromium session. Both run in one Docker container. Browser automation can break when X changes its website or restricts the account. See [ADR 0001](docs/adr/0001-browser-automation.md) for the reasoning behind this approach.

## Setup

Copy `.env.example` to `.env` and set `X_USERNAME` and `X_AUTH_TOKEN`. To get the token, log in to x.com in your browser and copy the `auth_token` cookie from DevTools → Application → Cookies → `https://x.com`.

```sh
cp .env.example .env
# Fill in the credentials, then:
docker compose build
docker compose run --rm xposter /app/orchestrator -seed
docker compose up -d
docker compose logs -f
```

Seed once on first installation. It marks the current feed as handled without posting anything; only later articles get posted. Seeding refuses to overwrite existing state, and normal startup refuses to run without it.

State and the browser session live in the `xposter-data` volume at `/data`. Keep this volume across upgrades. Stop the service before running one-off commands against it: both processes lock their files to prevent concurrent use.

## Configuration

See [.env.example](.env.example) for credentials and monitoring settings.

| Variable | Default | Purpose |
|---|---|---|
| `FEED_URL` | `https://www.zuidwestupdate.nl/feed/` | RSS feed |
| `POLL_INTERVAL` | `2m` | Time between polls; minimum `30s` |
| `POST_DELAY` | `30s` | Pause between posts |
| `MAX_AGE` | `24h` | Article age limit; greater than zero, at most `312h` |
| `ALLOWED_HOSTS` | Empty | Extra hosts allowed for articles and images, comma-separated |
| `DRY_RUN` | `false` | No automatic posts, state writes, alerts or heartbeats |

Downloads require HTTPS and use only the feed host or `ALLOWED_HOSTS`; private network addresses are blocked.

Chromium runs headed by default, with Dutch language settings and the Europe/Amsterdam timezone. Optional browser settings are `HEADLESS=false`, `SCREEN_SIZE=1920x1080`, `WINDOW_SIZE=1440x960` and `BROWSER_LANGUAGES=nl-NL,nl,en-US,en`.

## Posting and retries

Before posting, the service checks the account's recent posts for the article link. If that check fails, it waits. A new post counts as published only after X returns its ID.

Failed attempts retry with backoff. Ambiguous results, including interrupted posts, are checked against X before retrying. This reduces duplicates but cannot guarantee delivery.

Articles older than `MAX_AGE` are skipped and recorded as `missed`; expired retries become `failed_terminal`. Retries also require the article to remain in the feed. Articles that leave the feed before the service sees them cannot be recovered automatically. State history is kept indefinitely.

## Monitoring

```sh
docker compose ps
docker compose logs --tail=200 xposter
docker compose exec -T xposter node -e "fetch('http://127.0.0.1:8080/status').then(r => r.text()).then(console.log)"
docker compose exec -T xposter node -e "fetch('http://127.0.0.1:8081/ready').then(r => r.text()).then(console.log)"
```

The container healthcheck requires a recent successful poll and a valid X session. An unhealthy result does not restart the container. `/status` reports pending retries and missed or failed articles; `/ready` reports login status. Ports are internal to the container.

For e-mail alerts, set all four `GRAPH_*` variables and `ALERT_RECIPIENTS` in `.env`. The Entra app needs the `Mail.Send` application permission with admin consent. Partial configuration disables e-mail alerts.

Failed polls alert after `POLL_STALE_AFTER=15m`; poster readiness failures after `POSTER_NOT_READY_AFTER=30m`. Session checks run every 30–60 minutes, so an expired session can take about 90 minutes to trigger an alert. Active alerts repeat every `ALERT_REMINDER_INTERVAL=24h` and send a recovery message. Missed and terminally failed articles trigger one notification each.

Set `HEARTBEAT_URL` to ping an external monitor after each successful poll. This is needed to detect when the service itself is down.

## Maintenance

Pause or resume posting with `docker compose stop xposter` and `docker compose start xposter`. After a pause longer than `MAX_AGE`, review the feed, X and `/status` for missed articles.

If the session expires, replace `X_AUTH_TOKEN` in `.env` and run:

```sh
docker compose up -d --force-recreate xposter
```

`X_PASSWORD` is an optional fallback; `X_VERIFICATION` supplies an email or phone if X asks. Failed password logins have a 30-minute backoff. Avoid repeated restarts, which clear that backoff. If X restricts the account, stop the container and resolve it in a normal browser.

To retry one stored article that is still in the feed:

```sh
docker compose stop xposter
docker compose run --rm xposter /app/orchestrator -replay '<guid>'
docker compose start xposter
```

Find the GUID in `/data/state.json`. Replay queues a retry; the next poll checks for duplicates before posting. The required X lookback cannot exceed 14 days. Do not delete or reseed lost or corrupt state without checking which articles are already on X.

To run one diagnostic poll:

```sh
docker compose stop xposter
docker compose run --rm -e DRY_RUN=true xposter serve -once
docker compose start xposter
```

This may read X but does not post or write state. `DRY_RUN` applies to the Go process; manual calls to the poster's `/post` endpoint must also include `dryRun: true`.

Browser failure screenshots are kept for 14 days. Copy them with `docker compose cp xposter:/data/debug/. ./debug`.

## Development

Code and documentation are in English. Localized X interface patterns live in `poster/x-ui.json`.

```sh
go test ./...
cd poster
npm ci
npm test
npm run lint
```

From the repository root, run container and offline browser checks with:

```sh
docker build -t zw-xposter:test .
bash container/test.sh zw-xposter:test
```

The normal `npm test` suite needs no browser or credentials.

Licensed under [MIT](LICENSE).
