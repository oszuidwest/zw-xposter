# zw-xposter

Posts new [ZuidWest Update](https://www.zuidwestupdate.nl/) articles to [@zwupdate](https://x.com/zwupdate) with a link and image. Go polls RSS; Playwright posts through X. Both run in one Docker container.

## Setup

Copy `.env.example` to `.env` and set `X_USERNAME` and `X_AUTH_TOKEN` (the `auth_token` cookie from x.com → DevTools → Application → Cookies).

```sh
cp .env.example .env
# Fill in the credentials, then:
docker compose build
docker compose run --rm xposter /app/orchestrator -seed
docker compose up -d
```

Seed once to skip existing articles. Keep the `xposter-data` volume: it holds posting history and the browser session. Run one instance; stop it before one-off commands that use this volume. If state is lost or corrupt, check existing posts on X before reseeding.

Defaults: poll every two minutes, skip articles older than 24 hours. See [.env.example](.env.example) for settings, e-mail alerts and heartbeat monitoring.

## Operations

```sh
docker compose logs --tail=200 xposter
# Posting status and retries:
docker compose exec -T xposter node -e "fetch('http://127.0.0.1:8080/status').then(r => r.text()).then(console.log)"
```

If the X session expires, update `X_AUTH_TOKEN` and run `docker compose up -d --force-recreate xposter`. An unhealthy container does not restart automatically; set `HEARTBEAT_URL` to monitor downtime.

To retry an article still in the feed (up to 14 days old), find its GUID in `/data/state.json`:

```sh
docker compose stop xposter
docker compose run --rm xposter /app/orchestrator -replay '<guid>'
docker compose start xposter
```

## Development

```sh
go test ./...
cd poster
npm ci
npm test
npm run lint
```

Container checks (from the repo root): `docker build -t zw-xposter:test . && bash container/test.sh zw-xposter:test`.

Releases use Git tags (`vX.Y.Z`): pushing a tag runs security checks and publishes the Docker image. Stable releases update `latest`; prereleases do not.

Licensed under [MIT](LICENSE).
