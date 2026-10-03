// Synthetic variants share an envelope; testdata/contract keeps independent JSON fixtures.
export const account = { ownId: '100000000000000001', username: 'fixture_account' };
const recent = 'Fri Oct 02 13:30:00 +0000 2026';
const old = 'Fri Oct 02 11:00:00 +0000 2026';

function tweet(id, createdAt = recent, urls = []) {
  return {
    rest_id: id,
    core: { user_results: { result: { core: { screen_name: account.username } } } },
    legacy: {
      user_id_str: account.ownId,
      full_text: 'Synthetic post',
      created_at: createdAt,
      entities: { urls: urls.map((url) => ({ expanded_url: url })) },
    },
  };
}

function entry(result) {
  // Test absent itemType here; JSON pagination fixtures cover its presence.
  return { content: {
    entryType: 'TimelineTimelineItem',
    itemContent: { tweet_results: { result } },
  } };
}

export function timeline(...instructions) {
  return { data: { user: { result: { timeline_v2: { timeline: { instructions } } } } } };
}

function entries(...items) {
  return { type: 'TimelineAddEntries', entries: [
    ...items,
    { content: { entryType: 'TimelineTimelineCursor', cursorType: 'Bottom', value: 'synthetic-next' } },
  ] };
}

export function pinnedPage(isRecent = false) {
  const pinned = tweet(isRecent ? '900000000000000801' : '900000000000000201', isRecent ? recent : old,
    isRecent ? ['https://example.invalid/articles/pinned'] : []);
  const ordinary = tweet(isRecent ? '900000000000000802' : '900000000000000202', isRecent ? old : recent);
  return timeline({ type: 'TimelinePinEntry', entry: entry(pinned) }, entries(entry(ordinary)));
}

export function modulePage() {
  const posts = [tweet('900000000000000701'), tweet('900000000000000702', old)];
  return timeline(entries({ content: {
    entryType: 'TimelineTimelineModule',
    items: posts.map((post) => ({ item: { itemContent: entry(post).content.itemContent } })),
  } }));
}

export function nestedPage(retweet = false) {
  const post = tweet(retweet ? '900000000000000401' : '900000000000000301');
  const nested = { result: tweet(retweet ? '900000000000000402' : '900000000000000302', old) };
  if (retweet) post.legacy.retweeted_status_result = nested;
  else post.quoted_status_result = nested;
  return timeline(entries(entry(post)));
}

export function notePage() {
  const post = tweet('900000000000000501', recent, ['https://example.invalid/legacy']);
  post.note_tweet = { note_tweet_results: { result: {
    text: 'The complete text of a synthetic long post',
    entity_set: { urls: [{ expanded_url: 'https://example.invalid/articles/long' }] },
  } } };
  return timeline(entries(entry(post)));
}

// An older entry must not hide unreadable own posts; foreign posts and tombstones may be skipped.
export function unreadablePage(change, foreign = false) {
  const post = tweet('900000000000000811');
  change(post);
  const items = [entry(post)];
  if (foreign) {
    post.core.user_results.result.core.screen_name = 'other_account';
    post.legacy.user_id_str = '100000000000000002';
    items.push(entry({ __typename: 'TweetTombstone', tombstone: {} }));
  }
  return timeline(entries(...items, entry(tweet('900000000000000812', old))));
}
