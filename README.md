# zw-xposter

Posts new [ZuidWest Update](https://www.zuidwestupdate.nl/) articles to [@zwupdate](https://x.com/zwupdate), with video or an image. Go + Playwright in one Docker container.

## Setup

Copy [.env.example](.env.example) to `.env`. Set `X_USERNAME` and `X_AUTH_TOKEN` (x.com → DevTools → Application → Cookies → `auth_token`).

```sh
cp .env.example .env
# Fill in the credentials, then:
docker compose build
docker compose run --rm xposter /app/orchestrator -seed
docker compose up -d
```

Seed once to skip existing articles. Run one instance and keep the `xposter-data` volume (posting history and browser session). Stop the service before one-off commands. If state is lost, check X before reseeding to avoid duplicates.

Defaults: poll every two minutes, skip articles older than 24 hours. Optional settings, e-mail alerts and heartbeat monitoring are in `.env.example`.

Videos must be advertised in the RSS feed as enclosures: MP4, up to 512 MiB, 0.5 seconds–20 minutes. Add CDN and redirect hosts to `ALLOWED_HOSTS`. Failed videos never fall back to images. Large videos need about 1 GiB of temporary storage plus browser overhead; Compose provides the temporary storage.

Set `ELEVENLABS_API_KEY` with the **Speech to Text** permission for video posts. Each video is sent to [ElevenLabs Scribe v2](https://elevenlabs.io/docs/api-reference/speech-to-text/convert) with Dutch (`nld`) fixed as the language. The API generates an SRT file, which the browser attaches as Dutch captions before publishing. Transcription incurs ElevenLabs usage charges.

Missing credentials, failed transcription, empty/invalid captions or failed caption attachment prevent publication and use the normal retry flow. Temporary video files are removed after each attempt. Transcription has a 10-minute limit; the complete video request allows 30 minutes. Long videos can therefore delay subsequent feed checks and heartbeat updates. Image posts do not require ElevenLabs.

## Operations

```sh
docker compose logs --tail=200 xposter
```

Expired session: update `X_AUTH_TOKEN`, then run `docker compose up -d --force-recreate xposter`. Set `HEARTBEAT_URL` to detect downtime; unhealthy containers do not restart automatically.

Retry an article still in the feed (up to 14 days old), using its GUID from `/data/state.json`:

```sh
docker compose stop xposter
docker compose run --rm xposter /app/orchestrator -replay '<guid>'
docker compose start xposter
```

## Development

```sh
go test ./...
(cd poster && npm ci && npm test && npm run lint)
```

Full container and browser checks: `docker build -t zw-xposter:test . && bash container/test.sh zw-xposter:test`.

The browser integration test intercepts X requests and stubs ElevenLabs; it never publishes real posts or incurs transcription charges.

Run the **Release** workflow manually with a version such as `vX.Y.Z` (or `vX.Y.Z-prerelease`). It creates a missing tag at the selected commit, or reuses the existing tag, then runs security checks, publishes the image and creates the GitHub release with generated notes. Pushing a version tag also starts this workflow. Stable releases update `latest`; a release created by hand beforehand is kept.

Licensed under [MIT](LICENSE).
