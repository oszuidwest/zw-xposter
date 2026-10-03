# Contract fixtures

These fixtures are synthetic. They only model the `UserTweets`,
`TimelinePinEntry`, `TimelineReplaceEntry`, `TimelineTimelineModule`,
`quoted_status_result`, `retweeted_status_result`, `note_tweet`, cursor, and
`CreateTweet` fields used by the poster.

The raw JSON files pin pagination and the shared Go/Node HTTP contract.
`poster/timeline-fixtures.mjs` builds pinned, module, quoted, retweeted, long and
malformed posts from a shared envelope. Each call creates fresh objects, and
malformed-post cases retain an older same-page entry to test failure at the cutoff.

`browser-page.html` is a minimal offline composer and profile used by the browser
integration test. It exercises real clicks, typing and response handling, but
does not claim to reproduce X's current interface.

Replace them with anonymized DevTools responses when those become available.
Remove all cookies, tokens, `twid` values, and real account IDs before committing
replacement fixtures.
