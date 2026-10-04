# zw-xposter

Posts new [ZuidWest Update](https://www.zuidwestupdate.nl/) articles to [@zwupdate](https://x.com/zwupdate) with a link and their featured video, or an image for articles without video. Go polls RSS; Playwright posts through X. Both run in one Docker container.

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

Any RSS `<enclosure type="video/...">` takes priority over the article image (an MP4 enclosure is preferred when there are several), but only MP4 up to 512 MiB and between 0.5 seconds and 20 minutes long (X's limit for accounts without Premium) is supported. Featured article videos require [streekomroep-wp #253](https://github.com/oszuidwest/streekomroep-wp/pull/253), which reuses the linked fragment's cached MP4 enclosure; videos still being encoded or not yet cached are not advertised by the feed. Add the MP4 CDN hostname (and any redirect hosts) to `ALLOWED_HOSTS`.

MP4 files up to 512 MiB are streamed through temporary files; allow roughly 1 GiB of free temporary disk space for a maximum-size video. Downloads have five minutes and X upload/processing has ten minutes. The 512 MiB cap bounds local disk use; X itself accepts larger files. X still checks codecs, resolution and frame rate while processing (H.264 and AAC-LC work). A video that is not a readable `video/mp4`, is larger than 512 MiB, or falls outside the duration limits fails the article at once with an alert, because retrying cannot fix it. A video that does not record its duration, such as a fragmented MP4, is left to X. Other download failures (network errors, interrupted or empty downloads) and upload failures retry the article. Neither case publishes the image instead: only articles without a video enclosure use the existing image/text fallback.

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
# Offline HTTP-to-browser integration checks (requires Playwright Chromium):
node --test browser.integration.mjs
```

Container checks (from the repo root): `docker build -t zw-xposter:test . && bash container/test.sh zw-xposter:test`.

Releases use Git tags (`vX.Y.Z`): pushing a tag runs security checks and publishes the Docker image. Stable releases update `latest`; prereleases do not.

Licensed under [MIT](LICENSE).
