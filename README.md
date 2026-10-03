# zw-xposter

Posts new [ZuidWest Update](https://www.zuidwestupdate.nl/) articles to [@zwupdate](https://x.com/zwupdate), including a link and share image. Go polls the RSS feed; Playwright posts through a logged-in browser. Both run in one Docker container. Changes to X or an expired session can interrupt posting.

## Setup

Copy `.env.example` to `.env` and set `X_USERNAME` and `X_AUTH_TOKEN`. Get the token by logging in to x.com and copying the `auth_token` cookie from DevTools → Application → Cookies.

```sh
cp .env.example .env
# Fill in the credentials, then:
docker compose build
docker compose run --rm xposter /app/orchestrator -seed
docker compose up -d
```

Seed only on first installation: it marks existing articles as handled without posting them. Keep the `xposter-data` volume across upgrades; it holds posting history and the browser session. Run one instance and stop it before running one-off commands against this volume.

See [.env.example](.env.example) for configuration and optional e-mail alerts and heartbeat monitoring. By default, the feed is checked every two minutes and articles older than 24 hours are skipped. Failed posts are retried while eligible; uncertain outcomes are checked against X first to reduce duplicates.

## Operations

```sh
docker compose ps
docker compose logs --tail=200 xposter
# Pending retries and missed or failed articles:
docker compose exec -T xposter node -e "fetch('http://127.0.0.1:8080/status').then(r => r.text()).then(console.log)"
```

The healthcheck covers feed polling and the X session. An unhealthy result does not restart the container; configure `HEARTBEAT_URL` to detect downtime. If the session expires, replace `X_AUTH_TOKEN` in `.env` and run `docker compose up -d --force-recreate xposter`.

To retry an article, find its GUID in `/data/state.json`, then run:

```sh
docker compose stop xposter
docker compose run --rm xposter /app/orchestrator -replay '<guid>'
docker compose start xposter
```

Replay queues a duplicate-checked retry and requires the article to still be in the feed, with an X lookback of at most 14 days. Do not delete or reseed lost or corrupt state without checking existing posts on X.

For a diagnostic poll, stop the service, run `docker compose run --rm -e DRY_RUN=true xposter serve -once`, then start it again. This reads X without posting or writing state. Browser failure screenshots are kept for 14 days in `/data/debug`.

## Development

```sh
go test ./...
cd poster
npm ci
npm test
npm run lint
```

Run container and offline browser checks from the repository root with `docker build -t zw-xposter:test .` and `bash container/test.sh zw-xposter:test`. Fixtures in `testdata/contract` are synthetic; the normal `npm test` suite needs no browser or credentials. Localized X interface patterns live in `poster/x-ui.json`.

Licensed under [MIT](LICENSE).
