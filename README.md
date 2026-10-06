# zw-xposter

Posts new [ZuidWest Update](https://www.zuidwestupdate.nl/) articles to [@zwupdate](https://x.com/zwupdate), with video or an image. Runs Go + a native Node.js HTTP poster in Docker. No browser or Media Studio is required at runtime.

## Setup

Copy [.env.example](.env.example) to `.env` and set `X_USERNAME` and `X_AUTH_TOKEN` (x.com → DevTools → Application → Cookies → `auth_token`). Optional settings and monitoring are documented there.

```sh
cp .env.example .env
# Fill in the credentials, then:
docker compose build
docker compose run --rm xposter /app/orchestrator -seed
docker compose up -d
```

Seed once to skip existing articles. Run one instance and preserve `xposter-data` (posting history). Stop the service before one-off commands; check X before reseeding lost state to avoid duplicates.

Defaults: poll every two minutes; skip articles older than 24 hours.

Videos require MP4 RSS enclosures (up to 512 MiB, 0.5 seconds–20 minutes). Add CDN and redirect hosts to `ALLOWED_HOSTS`.

Publication tries **video with Dutch captions → video without captions → article image (`og:image`) → text with article link**. Only the feed's selected video enclosure is used. Media failures, including temporary download/upload failures and unsupported or empty videos, immediately advance the chain: timely publication takes priority over richer media. A successful fallback is final.

A video added to the feed within `VIDEO_REPLACE_WINDOW` (default 6 hours after publication; `0` disables) replaces a post published without one: the video post is published first, then the earlier post is deleted. Only posts this service published while the feed listed no video qualify, so video fallbacks and manual posts are never replaced. If the video cannot be posted (download or confirmed pre-click upload failure, or the retry window expires), the earlier post stays. Deletion requires a confirmed DeleteTweet for that exact post ID; failures recheck X first and alert after five attempts. Likes, reposts and replies on the deleted post are lost.

`ELEVENLABS_API_KEY` is optional. With a key that has **Speech to Text** permission, videos are sent to ElevenLabs for Dutch subtitles (paid usage). Without a key, videos publish without captions. Transcription errors, empty/invalid captions and definite caption attachment failures also permit uncaptioned video. Caption attachment recovery uses at most one clean video re-upload, without repeating transcription. Queueing, transcription, recovery and X requests share a 24-minute budget after at most five minutes receiving the video, within the client's 30-minute request. Later image/text requests have separate five-minute client budgets.

Session, account, general API failures and unconfirmed publication outcomes keep the existing retry/reconciliation behavior. Media-stage failures before CreateTweet permit immediate fallback; an unknown or lost CreateTweet response never does. The selected format and fallback reason are saved before advancing, so retries and restarts resume at that level after checking X for duplicates. Retries still at the captioned-video level may transcribe again and incur charges; once downgraded to uncaptioned video, they skip ElevenLabs. Logs report each fallback and the confirmed format; state keeps the fallback reason separately from the latest operational error.

The HTTP poster uploads and associates SRT captions before CreateTweet, using `text/plain` and the `subtitles` media category. Definite caption upload/association failures permit an uncaptioned video. Authentication failures (401/403), transient caption HTTP failures (408/429/5xx), ambiguous API errors and errors after publication starts retain captions. The existing `clicked` response field now means CreateTweet may have been sent: every such failure remains uncertain and requires checking X before retrying. The HTTP client never automatically retries publication.

`DRY_RUN=true` performs local download/preparation checks and logs the predicted format, including image/text fallback for unusable videos. Caption, upload and X API outcomes remain **unverified** (the orchestrator does not receive the API key). It makes no publication or paid transcription requests, persistent state writes, alerts or heartbeats. A manual `/post` with `dryRun: true` also skips authentication and uploads.

## X transport and migration

The poster uses X's **undocumented web API**, not the official developer API. It bootstraps a session from `X_AUTH_TOKEN`, verifies `X_USERNAME`, and reads the current GraphQL operation IDs and feature switches from X's HTML/static JavaScript without executing remote code. Cookies remain in memory and are never sent to the public script CDN. Session bootstrap is cached for five minutes; a changed main-bundle URL triggers fresh operation discovery. Unexpected schemas or incomplete timelines stop publication instead of guessing. X may change or restrict this interface at any time.

Uploads use INIT → APPEND (5 MiB chunks) → FINALIZE → STATUS, then CreateTweet; recent posts use cursor pagination and deletion uses DeleteTweet. Custom video thumbnails are **not implemented**: a successful metadata response alone did not prove the chosen image was used by a published post.

When upgrading from the browser version, keep the data volume and ensure `X_AUTH_TOKEN` is set. `X_PASSWORD`, `X_VERIFICATION`, `HEADLESS`, `SCREEN_SIZE`, `WINDOW_SIZE` and `BROWSER_LANGUAGES` are no longer used. The old profile may remain on disk but is not read or removed. There is no automated password login; expired or challenged sessions require a replacement cookie. `X_USER_AGENT` optionally overrides the default browser-compatible request header; it does not launch a browser.

## Operations

```sh
docker compose logs --tail=200 xposter
```

Expired session: update `X_AUTH_TOKEN`, then run `docker compose up -d --force-recreate xposter`. Use `HEARTBEAT_URL` for downtime alerts; unhealthy containers do not restart automatically.

X can also reject valid sessions with automation code 226 (even HTTP 200). The poster then reports not-ready and backs off for 30 minutes; it does not retry publication or downgrade the media. A live browserless upload/post/delete test succeeded, but a subsequent captioned-post test hit this restriction after upload/association succeeded. Continuous unattended posting is therefore not established by those tests. Do not deploy solely on the strength of the successful test.

## Development

```sh
go test ./...
(cd poster && npm ci && npm test && npm run lint)
docker build -t zw-xposter:test . && bash container/test.sh zw-xposter:test
```

Release: run the **Release** workflow with `vX.Y.Z` (or `vX.Y.Z-prerelease`), or push that tag. Stable releases update `latest`.

Licensed under [MIT](LICENSE).
