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

By default (`LINK_IN_REPLY=true`) the post contains only the headline with its video or image, and the article link follows directly in a reply to that post: `👉 Lees het hele verhaal: <link>` (`REPLY_TEXT` changes the text before the link). Text-only fallback posts keep the link in the post itself, since its preview card is their only image. `LINK_IN_REPLY=false` restores one post with headline and link.

The post is saved as soon as X confirms it, so a failed reply never republishes the article: only the reply is retried, with the same backoff and duplicate check as posts, until MAX_AGE. After that, a `failed_terminal` alert names the post so someone can add the link by hand. The duplicate check reads the profile's **Posts** and **Replies** tabs: an own reply with the article link means the article is done. While a post awaits its link reply, only a reply under that post counts. Another feed entry for the same article waits while an earlier entry's headline is on X without the link, or may be after an uncertain attempt; it counts as posted once the link is on X. If that entry expires with its alert instead, the waiting entry publishes the article itself within MAX_AGE; once its headline is on X, a second alert names the earlier post to delete, or says its outcome is unknown, so it is not also completed by hand. An expiring entry without a post of its own points to the other entry instead, so the article is not posted again by hand while that headline is on X. An unconfirmed post is not retried and is reconciled only by its article link: without it, it stays uncertain until MAX_AGE and then raises a `failed_terminal` alert.

If the feed adds a video to an article that was posted without one, the post stays unchanged and a one-time alert names the post and the video, so someone can decide by hand. This applies within MAX_AGE, not to video fallbacks or entries saved before this check existed.

`ELEVENLABS_API_KEY` is optional. With a key that has **Speech to Text** permission, videos are sent to ElevenLabs for Dutch subtitles (paid usage). Without a key, videos publish without captions. Transcription errors, empty/invalid captions and caption attachment failures also permit uncaptioned video. Caption attachment recovery uses one clean composer and at most one video re-upload, without repeating transcription. Queueing, transcription, recovery and browser work share a 24-minute budget after at most five minutes receiving the video, within the client's 30-minute request. Later image/text requests have separate five-minute client budgets.

Login, account, general browser failures and unconfirmed outcomes keep the existing retry/reconciliation behavior. Only confirmed media failures before clicking Post permit immediate fallback; unknown or lost responses never do. The selected format and fallback reason are saved before advancing, so retries and restarts resume at that level after checking X for duplicates. Retries still at the captioned-video level may transcribe again and incur charges; once downgraded to uncaptioned video, they skip ElevenLabs. Logs report each fallback and the confirmed format; state keeps the fallback reason separately from the latest operational error.

X uploads SRT captions after the Post click; `text/plain` selects its `subtitles` media category. Confirmed caption upload/association failures log X's error and save an uncaptioned retry. Authentication failures (401/403), transient HTTP failures (408/429/5xx), ambiguous API errors and errors after publication starts retain captions. Every post-click failure remains uncertain and requires checking X before retrying.

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
