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

Publication tries **video with Dutch captions → video without captions → article image (`og:image`) → text with article link**. Only the feed's selected video enclosure is used. Media failures, including temporary download/upload failures and unsupported or empty videos, immediately advance the chain: timely publication takes priority over richer media. A successful fallback is final.

`ELEVENLABS_API_KEY` is optional. With a key that has **Speech to Text** permission, videos are sent to ElevenLabs for Dutch subtitles (paid usage). Without a key, videos publish without captions. Transcription errors, empty/invalid captions and caption attachment failures also permit uncaptioned video. Caption attachment recovery uses one clean composer and at most one video re-upload, without repeating transcription. Queueing, transcription, recovery and browser work share a 24-minute budget after at most five minutes receiving the video, within the client's 30-minute request. Later image/text requests have separate five-minute client budgets.

Login, account, general browser failures and unconfirmed outcomes keep the existing retry/reconciliation behavior. Only confirmed media failures before clicking Post permit fallback; unknown or lost responses never do. The selected format and fallback reason are saved before advancing, so retries and restarts resume at that level after checking X for duplicates. Retries still at the captioned-video level may transcribe again and incur charges; once downgraded to uncaptioned video, they skip ElevenLabs. Logs report each fallback and the confirmed format; state keeps the fallback reason separately from the latest operational error.

`DRY_RUN=true` performs local download/preparation checks and logs the predicted format, including image/text fallback for unusable videos. Caption, upload and browser outcomes remain **unverified** (the orchestrator does not receive the API key). It makes no publication or paid transcription requests, persistent state writes, alerts or heartbeats.

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
