import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  createTimelineCollector,
  isFirstUserTweetsPage,
  isUserTimelineResponse,
  parseCreateTweetResponse,
  parseUserTweetsPayload,
  postErrorResponse,
} from './timeline.mjs';
import { account, timeline, pinnedPage, modulePage, nestedPage, notePage, unreadablePage } from './timeline-fixtures.mjs';

const cutoff = Date.parse('2026-10-02T12:00:00.000Z');

async function fixture(name) {
  const file = new URL(`../testdata/contract/${name}`, import.meta.url);
  return JSON.parse(await readFile(file, 'utf8'));
}

async function collectFirstPage(body) {
  const timeline = createTimelineCollector({ ...account, cutoff });
  timeline.add(await body, { firstPage: true });
  return timeline;
}

test('collects an ordinary timeline over multiple pages until the cutoff', async () => {
  const timeline = await collectFirstPage(fixture('user-tweets-page-1.json'));
  assert.equal(timeline.complete(), false);

  timeline.add(await fixture('user-tweets-page-2.json'));
  assert.deepEqual(timeline.result(), await fixture('recent-complete.json'));
});

test('a continuation page cannot complete a timeline without the first page', async () => {
  const timeline = createTimelineCollector({ ...account, cutoff });
  timeline.add(await fixture('user-tweets-page-2.json'));

  assert.equal(timeline.result().complete, false);
});

// Check which page shapes establish completeness and contribute searchable posts.
for (const [name, page, complete, ids, urls] of [
  ['an old pinned entry does not complete the requested range',
    pinnedPage, false, ['900000000000000202']],
  ['a recent pinned post is collected for the duplicate check',
    () => pinnedPage(true), true, ['900000000000000801'], ['https://example.invalid/articles/pinned']],
  ['an old quoted post is not treated as a top-level timeline entry',
    nestedPage, false, ['900000000000000301']],
  ['a tweet nested in a retweet is ignored', () => nestedPage(true), false, []],
  ['an empty page with a replaced Bottom cursor reaches the end',
    () => fixture('user-tweets-end.json'), true, []],
  ['a Bottom terminate instruction reaches the end',
    () => timeline({ type: 'TimelineTerminateTimeline', direction: 'Bottom' }), true, []],
  ['a page without a cursor or terminate instruction is incomplete',
    () => fixture('user-tweets-no-cursor.json'), false],
  ['conversation modules are searchable but never establish the cutoff boundary',
    modulePage, false, ['900000000000000701']],
]) {
  test(name, async () => {
    const result = (await collectFirstPage(page())).result();
    assert.equal(result.complete, complete);
    if (ids) assert.deepEqual(result.posts.map((post) => post.id), ids);
    if (urls) assert.deepEqual(result.posts[0].urls, urls);
    if (ids?.length === 0) assert.deepEqual(result, { posts: [], complete });
  });
}

function firstEntryResult(body) {
  const [instruction] = body.data.user.result.timeline_v2.timeline.instructions;
  const content = instruction.entry?.content || instruction.entries[0].content;
  return content.itemContent.tweet_results.result;
}

function removeAuthor(post) {
  delete post.core;
  delete post.legacy.user_id_str;
}

async function withFirstPost(source, change) {
  const body = await source;
  change(firstEntryResult(body));
  return body;
}

function assertUnreadable(body, pattern = /unreadable own post/, ownId = account.ownId) {
  const timeline = createTimelineCollector({ ...account, ownId, cutoff });
  assert.throws(() => timeline.add(body, { firstPage: true }), pattern);
  assert.equal(timeline.result().complete, false);
}

test('an own post without full_text makes the result incomplete', () => {
  assertUnreadable(unreadablePage((post) => { delete post.legacy.full_text; }));
});

for (const [name, createdAt] of [
  ['an unparseable', 'not a date'],
  ['a missing', undefined],
  ['a null', null],
  ['a numeric', 0],
  ['an empty', ''],
  ['a bare-number string', '0'],
]) {
  test(`an own post with ${name} created_at makes the result incomplete`, () => {
    const body = unreadablePage((post) => { post.legacy.created_at = createdAt; });
    assertUnreadable(body);
  });
}

test('an unreadable own pinned post makes the result incomplete', () => {
  const body = pinnedPage(true);
  firstEntryResult(body).legacy.created_at = null;
  assertUnreadable(body);
});

test('unreadable posts by others do not make the result incomplete', async () => {
  const timeline = await collectFirstPage(unreadablePage((post) => {
    delete post.legacy.full_text;
    delete post.legacy.entities;
  }, true));

  assert.deepEqual(timeline.result(), { posts: [], complete: true });
});

test('a post without a readable author cannot be passed by an older page', async () => {
  const timeline = createTimelineCollector({ ...account, cutoff });
  const body = await withFirstPost(fixture('user-tweets-page-1.json'), removeAuthor);
  assert.throws(() => timeline.add(body, { firstPage: true }), /without a readable author/);
  timeline.add(await fixture('user-tweets-page-2.json'));

  assert.equal(timeline.result().complete, false);
});

test('an end marker cannot complete a page with an unreadable author', async () => {
  const body = await withFirstPost(fixture('user-tweets-page-1.json'), removeAuthor);
  body.data.user.result.timeline_v2.timeline.instructions.push({
    type: 'TimelineTerminateTimeline', direction: 'Bottom',
  });
  assertUnreadable(body, /without a readable author/);
});

test('an own post with a handle but no legacy data fails closed', async () => {
  assertUnreadable(await withFirstPost(fixture('user-tweets-page-1.json'), (post) => {
    delete post.legacy;
  }));
});

test('tombstones and non-tweet items are still skipped', async () => {
  for (const result of [undefined, { __typename: 'TweetTombstone' }]) {
    const body = await fixture('user-tweets-page-1.json');
    body.data.user.result.timeline_v2.timeline.instructions[0].entries[0]
      .content.itemContent.tweet_results.result = result;
    const { posts } = parseUserTweetsPayload(body, account);
    assert.deepEqual(posts.map((post) => post.id), ['900000000000000102']);
  }
});

for (const [name, ownId, change] of [
  ['neither a user ID nor a handle', account.ownId, removeAuthor],
  ['an empty user result and no user ID', account.ownId, (post) => {
    post.core.user_results.result = {};
    delete post.legacy.user_id_str;
  }],
  ['only a user ID while the own ID is unknown', '', (post) => {
    delete post.core;
  }],
  ['only a post ID', account.ownId, (post) => {
    delete post.core;
    delete post.legacy;
  }],
]) {
  test(`a post with ${name} makes the result incomplete`, async () => {
    assertUnreadable(await withFirstPost(fixture('user-tweets-page-1.json'), change), /without a readable author/, ownId);
  });
}

test('pinned and module posts without a readable author make the result incomplete', async () => {
  assertUnreadable(await withFirstPost(pinnedPage(true), removeAuthor), /without a readable author/);

  const module = modulePage();
  const [instruction] = module.data.user.result.timeline_v2.timeline.instructions;
  removeAuthor(instruction.entries[0].content.items[0].item.itemContent.tweet_results.result);
  assertUnreadable(module, /without a readable author/);
});

for (const [name, ownId, change, ids] of [
  ['recognizes an own post by the user ID without a handle', account.ownId, (post) => {
    delete post.core;
  }, ['900000000000000101', '900000000000000102']],
  ['recognizes an own post by the handle without a user ID', account.ownId, (post) => {
    delete post.legacy.user_id_str;
  }, ['900000000000000101', '900000000000000102']],
  ['recognizes an own post by the legacy handle without a user ID', account.ownId, (post) => {
    delete post.legacy.user_id_str;
    post.core.user_results.result = { legacy: { screen_name: 'Fixture_Account' } };
  }, ['900000000000000101', '900000000000000102']],
  ['recognizes an own post by the handle while the own ID is unknown', '', () => {},
    ['900000000000000101', '900000000000000102']],
  ['skips a post known to be by another author through another user ID', account.ownId, (post) => {
    delete post.core;
    post.legacy.user_id_str = '100000000000000002';
  }, ['900000000000000102']],
  ['skips a post known to be by another author through another handle', account.ownId, (post) => {
    delete post.legacy.user_id_str;
    post.core.user_results.result.core.screen_name = 'other_account';
  }, ['900000000000000102']],
]) {
  test(name, async () => {
    const body = await withFirstPost(fixture('user-tweets-page-1.json'), change);
    const { posts } = parseUserTweetsPayload(body, { ...account, ownId });

    assert.deepEqual(posts.map((post) => post.id), ids);
  });
}

for (const [name, change] of [
  ['no entities', (post) => {
    delete post.legacy.entities;
  }],
  ['null entities', (post) => {
    post.legacy.entities = null;
  }],
  ['entities without a urls list', (post) => {
    post.legacy.entities = { media: [] };
  }],
  ['urls that are not a list', (post) => {
    post.legacy.entities.urls = { expanded_url: 'https://example.invalid/articles/newest' };
  }],
  ['a link without expanded_url', (post) => {
    post.legacy.entities.urls = [{ url: 'https://t.co/newest' }];
  }],
  ['an empty expanded_url', (post) => {
    post.legacy.entities.urls[0].expanded_url = ' ';
  }],
  ['a non-string expanded_url', (post) => {
    post.legacy.entities.urls[0].expanded_url = 42;
  }],
]) {
  test(`an own post with ${name} makes the result incomplete`, async () => {
    assertUnreadable(await withFirstPost(fixture('user-tweets-page-1.json'), change));
  });
}

for (const [name, change] of [
  ['note urls that are not a list', (note) => {
    note.entity_set.urls = {};
  }],
  ['null note urls', (note) => {
    note.entity_set.urls = null;
  }],
  ['a note link without expanded_url', (note) => {
    note.entity_set.urls = [{ url: 'https://t.co/long' }];
  }],
]) {
  test(`an own long post with ${name} makes the result incomplete`, async () => {
    assertUnreadable(await withFirstPost(notePage(), (post) => {
      change(post.note_tweet.note_tweet_results.result);
    }));
  });
}

test('media t.co links do not need an expanded article link', async () => {
  const body = await fixture('user-tweets-page-1.json');
  const [withLink, mediaOnly] = body.data.user.result.timeline_v2.timeline.instructions[0].entries
    .slice(0, 2)
    .map((entry) => entry.content.itemContent.tweet_results.result.legacy);
  const media = [{ url: 'https://t.co/photo', expanded_url: 'https://x.com/fixture_account/status/1/photo/1' }];
  withLink.full_text = 'Newest article https://t.co/newest https://t.co/photo';
  withLink.entities.media = media;
  mediaOnly.full_text = 'Photo only https://t.co/photo';
  mediaOnly.entities = { urls: [], media };
  const { posts } = parseUserTweetsPayload(body, account);

  assert.deepEqual(posts.map((post) => post.urls), [['https://example.invalid/articles/newest'], []]);
});

test('a long post without note link entities keeps its legacy links', async () => {
  const body = await withFirstPost(notePage(), (post) => {
    delete post.note_tweet.note_tweet_results.result.entity_set;
  });
  const { posts } = parseUserTweetsPayload(body, account);

  assert.deepEqual(posts[0].urls, ['https://example.invalid/legacy']);
});

test('uses note_tweet text and expanded URLs for long posts', () => {
  const parsed = parseUserTweetsPayload(notePage(), account);

  assert.equal(parsed.posts[0].text, 'The complete text of a synthetic long post');
  assert.deepEqual(parsed.posts[0].urls, [
    'https://example.invalid/legacy',
    'https://example.invalid/articles/long',
  ]);
});

test('recognizes only a UserTweets request without a cursor as the first page', () => {
  assert.equal(isFirstUserTweetsPage('https://api.x.invalid/UserTweets?variables=%7B%22count%22%3A20%7D'), true);
  assert.equal(isFirstUserTweetsPage('https://api.x.invalid/UserTweets?variables=%7B%22cursor%22%3A%22next%22%7D'), false);
  assert.equal(isFirstUserTweetsPage('https://api.x.invalid/UserTweets?variables=not-json'), false);
  assert.equal(isFirstUserTweetsPage('https://api.x.invalid/UserTweets'), false);
});

test('recognizes both operation names for the own profile timeline', () => {
  assert.equal(isUserTimelineResponse('https://x.com/i/api/graphql/abc/UserTweets?variables=%7B%7D'), true);
  assert.equal(isUserTimelineResponse('https://x.com/i/api/graphql/abc/UserOriginalsTimeline?variables=%7B%7D'), true);
  assert.equal(isUserTimelineResponse('https://x.com/i/api/graphql/abc/UserTweetsAndReplies'), false);
  assert.equal(isUserTimelineResponse('https://x.com/i/api/graphql/abc/HomeTimeline'), false);
  assert.equal(isUserTimelineResponse('not a url'), false);
});

test('reports why a timeline is incomplete', async () => {
  const continuation = createTimelineCollector({ ...account, cutoff });
  continuation.add(await fixture('user-tweets-page-2.json'));
  assert.match(continuation.incompleteReason(), /first UserTweets page was not seen/);

  const timeline = await collectFirstPage(fixture('user-tweets-page-1.json'));
  assert.match(timeline.incompleteReason(), /cutoff/);
  assert.equal(timeline.failed(), false);

  timeline.fail('UserTweets returned HTTP 429');
  timeline.fail('a later failure');
  assert.equal(timeline.incompleteReason(), 'UserTweets returned HTTP 429');
  assert.equal(timeline.failed(), true);
});

for (const reason of [undefined, '']) {
  test(`a collection failure with ${reason === undefined ? 'no argument' : 'an empty message'} overrides completeness`, async () => {
    const timeline = await collectFirstPage(fixture('user-tweets-end.json'));
    assert.equal(timeline.result().complete, true);
    timeline.fail(reason);
    assert.equal(timeline.failed(), true);
    assert.equal(timeline.result().complete, false);
  });
}

test('a GraphQL pagination error makes the result incomplete', async () => {
  const timeline = await collectFirstPage(fixture('user-tweets-page-1.json'));
  const failedPage = await fixture('user-tweets-pagination-error.json');

  assert.throws(
    () => timeline.add(failedPage),
    /Synthetic pagination failure/,
  );

  assert.equal(timeline.result().complete, false);
  assert.match(timeline.incompleteReason(), /Synthetic pagination failure/);
});

test('parses a CreateTweet response with a rest_id', async () => {
  const result = parseCreateTweetResponse(await fixture('create-tweet-with-id.json'), 'fallback_account');

  assert.deepEqual(result, {
    id: '900000000000000601',
    url: 'https://x.com/fixture_account/status/900000000000000601',
  });
});

test('reads the CreateTweet handle like timeline posts, including the legacy field', () => {
  const body = {
    data: { create_tweet: { tweet_results: { result: {
      rest_id: '900000000000000602',
      core: { user_results: { result: { legacy: { screen_name: 'legacy_account' } } } },
    } } } },
  };

  assert.equal(
    parseCreateTweetResponse(body, 'fallback_account').url,
    'https://x.com/legacy_account/status/900000000000000602',
  );
});

test('rejects a CreateTweet response without a rest_id', async () => {
  const body = await fixture('create-tweet-without-id.json');

  assert.throws(
    () => parseCreateTweetResponse(body, 'fixture_account'),
    /X rejected the post: Synthetic rejection/,
  );
});

test('serializes post errors with click state, stage and caption outcome', async () => {
  const before = new Error('composer did not become ready');
  const after = new Error('X rejected the post: Synthetic rejection (after clicking post; it may be on X)');
  after.clicked = true;
  const video = Object.assign(new Error('video processing failed: {"message":"Synthetic encoding failure"}'), {
    stage: 'video', captions: 'none', fallbackReason: 'caption generation: ElevenLabs transcription returned HTTP 503',
  });

  assert.deepEqual(postErrorResponse(before), await fixture('post-error-before-click.json'));
  assert.deepEqual(postErrorResponse(after), await fixture('post-error-after-click.json'));
  assert.deepEqual(postErrorResponse(video), await fixture('post-error-video-stage.json'));
});
