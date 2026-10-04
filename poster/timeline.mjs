// X response parsing and timeline collection without browser or process state.

function errorMessages(body) {
  return body?.errors?.map((error) => error?.message).filter(Boolean).join('; ');
}

function screenName(result) {
  const user = result?.core?.user_results?.result;
  return user?.core?.screen_name || user?.legacy?.screen_name;
}

function timelineInstructions(body) {
  if (body?.errors?.length) {
    throw new Error(`UserTweets returned errors: ${errorMessages(body) || 'unknown error'}`);
  }

  const instructions = body?.data?.user?.result?.timeline_v2?.timeline?.instructions
    || body?.data?.user?.result?.timeline?.timeline?.instructions;
  if (!Array.isArray(instructions)) {
    throw new Error('UserTweets response has no timeline instructions');
  }
  return instructions;
}

// X's created_at format, e.g. "Fri Oct 02 13:30:00 +0000 2026".
const CREATED_AT = /^[A-Z][a-z]{2} [A-Z][a-z]{2} \d{2} \d{2}:\d{2}:\d{2} [+-]\d{4} \d{4}$/;

function tweetResult(itemContent) {
  let result = itemContent?.tweet_results?.result;
  if (result?.__typename === 'TweetWithVisibilityResults') result = result.tweet;
  return result;
}

// expandedUrls returns expanded t.co links, or null when entities are unreadable.
// Media links and absent long-post links need no expansion; legacy links do.
function expandedUrls(legacy, note) {
  const noteUrls = note?.entity_set?.urls;
  const lists = [legacy?.entities?.urls, noteUrls === undefined ? [] : noteUrls];
  if (!lists.every(Array.isArray)) return null;
  const urls = lists.flat().map((url) => url?.expanded_url);
  if (!urls.every((url) => typeof url === 'string' && url.trim() !== '')) return null;
  return [...new Set(urls)];
}

function parseTweet(result, ownId, username) {
  const legacy = result?.legacy;
  const handle = screenName(result);
  const authorId = legacy?.user_id_str;
  const own = (ownId && authorId === ownId)
    || handle?.toLowerCase() === username.toLowerCase();
  const foreign = (ownId && authorId && authorId !== ownId) || (handle && !own);
  // An unknown author could hide a duplicate; tombstones and non-tweets are skipped.
  if (!own && !foreign && (result?.rest_id || legacy)) {
    throw new Error(`UserTweets has a post without a readable author: ${result.rest_id || 'no id'}`);
  }
  if (!own || legacy?.retweeted_status_result) return null;

  // An unreadable own post could be the duplicate, so it cannot prove the cutoff.
  // Without expanded links, a t.co URL cannot match an article.
  const createdAt = CREATED_AT.test(legacy?.created_at) ? new Date(legacy.created_at) : new Date(NaN);
  const note = result.note_tweet?.note_tweet_results?.result;
  const urls = expandedUrls(legacy, note);
  if (!result.rest_id || typeof legacy?.full_text !== 'string' || Number.isNaN(createdAt.getTime()) || !urls) {
    throw new Error(`UserTweets has an unreadable own post: ${result.rest_id || 'no id'}`);
  }

  return {
    id: result.rest_id,
    url: `https://x.com/${handle || username}/status/${result.rest_id}`,
    text: note?.text || legacy.full_text,
    urls,
    createdAt: createdAt.toISOString(),
  };
}

// parseUserTweetsPayload reads entries in response order. Pinned entries and
// conversation modules are searchable; quoted and retweeted tweets are not.
// Only top-level entries can prove the cutoff.
export function parseUserTweetsPayload(body, { ownId = '', username }) {
  const posts = [];
  let oldestTopLevel = Infinity;
  let reachedEnd = false;
  let hasBottomCursor = false;
  let hasTweetOrModuleEntry = false;

  for (const instruction of timelineInstructions(body)) {
    if (instruction.type === 'TimelineTerminateTimeline' && instruction.direction === 'Bottom') {
      reachedEnd = true;
      continue;
    }
    if (instruction.type === 'TimelinePinEntry') {
      // Pinned posts do not establish how far back the timeline was read.
      const post = parseTweet(tweetResult(instruction.entry?.content?.itemContent), ownId, username);
      if (post) posts.push(post);
      continue;
    }

    let entries = [];
    if (instruction.type === 'TimelineAddEntries' && Array.isArray(instruction.entries)) {
      entries = instruction.entries;
    } else if (instruction.type === 'TimelineReplaceEntry' && instruction.entry) {
      entries = [instruction.entry];
    }

    for (const entry of entries) {
      const content = entry?.content;
      if (content?.entryType === 'TimelineTimelineCursor' && content.cursorType === 'Bottom') {
        hasBottomCursor = true;
        continue;
      }

      if (content?.entryType === 'TimelineTimelineItem') {
        hasTweetOrModuleEntry = true;
        const post = parseTweet(tweetResult(content.itemContent), ownId, username);
        if (post) {
          posts.push(post);
          oldestTopLevel = Math.min(oldestTopLevel, Date.parse(post.createdAt));
        }
        continue;
      }

      if (content?.entryType === 'TimelineTimelineModule') {
        hasTweetOrModuleEntry = true;
        for (const moduleItem of content.items || []) {
          const post = parseTweet(tweetResult(moduleItem?.item?.itemContent), ownId, username);
          if (post) posts.push(post);
        }
      }
    }
  }
  if (hasBottomCursor && !hasTweetOrModuleEntry) reachedEnd = true;

  return { posts, oldestTopLevel, reachedEnd };
}

// Both operation names serve the account's profile timeline.
const USER_TIMELINE_OPERATIONS = new Set(['UserTweets', 'UserOriginalsTimeline']);

export function isUserTimelineResponse(responseURL) {
  try {
    return USER_TIMELINE_OPERATIONS.has(new URL(responseURL).pathname.split('/').pop());
  } catch {
    return false;
  }
}

// Parsed variables without a cursor identify the first profile-timeline page.
export function isFirstUserTweetsPage(responseURL) {
  try {
    const raw = new URL(responseURL).searchParams.get('variables');
    if (!raw) return false;
    const variables = JSON.parse(raw);
    return variables !== null
      && typeof variables === 'object'
      && !Object.hasOwn(variables, 'cursor');
  } catch {
    return false;
  }
}

// Collect pages; completeness requires the first page, a boundary and no failures.
export function createTimelineCollector({ ownId = '', username, cutoff }) {
  const found = new Map();
  let reachedBoundary = false;
  let sawFirstPage = false;
  let failure = '';

  // incompleteReason is empty once the pages prove the range was inspected.
  const incompleteReason = () => failure
    || (!sawFirstPage && 'the first UserTweets page was not seen')
    || (!reachedBoundary && 'neither the cutoff nor the end of the timeline was reached')
    || '';
  const fail = (reason) => {
    failure ||= reason || 'unknown UserTweets failure';
  };

  return {
    add(body, { firstPage = false } = {}) {
      let page;
      try {
        page = parseUserTweetsPayload(body, { ownId, username });
      } catch (error) {
        fail(error.message);
        throw error;
      }
      if (firstPage) sawFirstPage = true;
      for (const post of page.posts) {
        if (!found.has(post.id)) found.set(post.id, post);
      }
      if (page.oldestTopLevel < cutoff || page.reachedEnd) reachedBoundary = true;
    },

    fail,
    failed: () => failure !== '',
    incompleteReason,
    complete: () => !incompleteReason(),

    result() {
      return {
        posts: [...found.values()]
          .filter((post) => Date.parse(post.createdAt) >= cutoff)
          .sort((a, b) => b.createdAt.localeCompare(a.createdAt)),
        complete: !incompleteReason(),
      };
    },
  };
}

// Require a tweet ID; otherwise report X's rejection message.
export function parseCreateTweetResponse(body, username) {
  const result = body?.data?.create_tweet?.tweet_results?.result;
  const id = result?.rest_id;
  if (!id) {
    const reason = errorMessages(body) || 'no tweet id in response';
    throw new Error(`X rejected the post: ${reason}`);
  }

  return { id, url: `https://x.com/${screenName(result) || username}/status/${id}` };
}

// Preserve the error and click state so the orchestrator can reconcile uncertain outcomes.
export function postErrorResponse(error) {
  return { error: error.message, clicked: Boolean(error.clicked) };
}
