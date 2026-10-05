# zw-xposter

Posts new [ZuidWest Update](https://www.zuidwestupdate.nl/) articles to [@zwupdate](https://x.com/zwupdate), with video or an image. Runs Go + Playwright in Docker.

## Setup

Copy [.env.example](.env.example) to `.env` and set `X_USERNAME` and `X_AUTH_TOKEN` (x.com → DevTools → Application → Cookies → `auth_token`). Optional settings and monitoring are documented there.

```sh
cp .env.example .env
# Fill in the credentials, then:
docker compose build
docker compose run --rm xposter /app/orchestrator -seed
docker compose up -d
```

Seed once to skip existing articles. Run one instance and preserve `xposter-data` (posting history and browser session). Stop the service before one-off commands; check X before reseeding lost state to avoid duplicates.

Defaults: poll every two minutes; skip articles older than 24 hours.

Videos require MP4 RSS enclosures (up to 512 MiB, 0.5 seconds–20 minutes). Add CDN and redirect hosts to `ALLOWED_HOSTS`.

Set `ELEVENLABS_API_KEY` with **Speech to Text** permission for video posts. Videos are sent to ElevenLabs for Dutch subtitles (paid usage). Video or subtitle failures are retried without falling back to images.

## Operations

```sh
docker compose logs --tail=200 xposter
```

Expired session: update `X_AUTH_TOKEN`, then run `docker compose up -d --force-recreate xposter`. Use `HEARTBEAT_URL` for downtime alerts; unhealthy containers do not restart automatically.

## Development

```sh
go test ./...
(cd poster && npm ci && npm test && npm run lint)
docker build -t zw-xposter:test . && bash container/test.sh zw-xposter:test
```

Release: run the **Release** workflow with `vX.Y.Z` (or `vX.Y.Z-prerelease`), or push that tag. Stable releases update `latest`.

Licensed under [MIT](LICENSE).
