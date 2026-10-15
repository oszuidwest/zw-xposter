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

// X omits empty entity lists. A missing urls list means no links only when every t.co link
// in the text is a media link; otherwise it stays undefined and the post is unreadable.
function legacyUrls(legacy) {
  const entities = legacy?.entities;
  if (entities?.urls !== undefined) return entities.urls;
  if (typeof entities !== 'object' || entities === null || Array.isArray(entities)) return undefined;
  const media = entities.media === undefined ? [] : entities.media;
  if (!Array.isArray(media) || typeof legacy.full_text !== 'string') return undefined;
  const mediaLinks = new Set(media.map((item) => item?.url));
  const links = legacy.full_text.match(/https:\/\/t\.co\/\w+/g) || [];
  return links.every((link) => mediaLinks.has(link)) ? [] : undefined;
}

// Unreadable link entities return null so duplicate checks fail closed.
// Media links and absent long-post links need no expansion; legacy links do.
function expandedUrls(legacy, note) {
  const noteUrls = note?.entity_set?.urls;
  const lists = [legacyUrls(legacy), noteUrls === undefined ? [] : noteUrls];
  if (!lists.every(Array.isArray)) return null;
  const urls = lists.flat().map((url) => url?.expanded_url);
  if (!urls.every((url) => typeof url === 'string' && url.trim() !== '')) return null;
  return [...new Set(urls)];
}

// Self-replies keep their parent's URL; replies to others are not posts of the account.
// An unknown parent author could hide a link reply, so it fails closed.
function replyParent(legacy, ownId, username, handle) {
  const parentId = legacy.in_reply_to_status_id_str;
  if (parentId === undefined || parentId === null) return {};
  const parentUser = legacy.in_reply_to_user_id_str;
  const parentHandle = legacy.in_reply_to_screen_name;
  const own = (ownId && parentUser === ownId)
    || (typeof parentHandle === 'string' && parentHandle.toLowerCase() === username.toLowerCase());
  const foreign = (ownId && parentUser && parentUser !== ownId) || (parentHandle && !own);
  if (!/^\d+$/.test(parentId) || (!own && !foreign)) return { unreadable: true };
  if (!own) return { foreign: true };
  return { replyTo: `https://x.com/${parentHandle || handle || username}/status/${parentId}` };
}

// Returns null for others' posts, retweets and tombstones. Otherwise time is the post's timeline
// position, and post is unset for own replies to others.
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

  // Unreadable own posts could hide duplicates, even beyond the cutoff.
  const createdAt = CREATED_AT.test(legacy?.created_at) ? new Date(legacy.created_at) : new Date(NaN);
  const note = result.note_tweet?.note_tweet_results?.result;
  const urls = expandedUrls(legacy, note);
  const parent = legacy ? replyParent(legacy, ownId, username, handle) : {};
  if (!result.rest_id || typeof legacy?.full_text !== 'string' || Number.isNaN(createdAt.getTime()) || !urls || parent.unreadable) {
    throw new Error(`UserTweets has an unreadable own post: ${result.rest_id || 'no id'}`);
  }

  const time = createdAt.getTime();
  if (parent.foreign) return { time };
  return { time, post: {
    id: result.rest_id,
    url: `https://x.com/${handle || username}/status/${result.rest_id}`,
    text: note?.text || legacy.full_text,
    urls,
    createdAt: createdAt.toISOString(),
    ...(parent.replyTo && { replyTo: parent.replyTo }),
  } };
}

// Search pinned posts and conversation modules, but skip quotes and retweets.
// Unpinned entries prove the cutoff; a conversation module sorts by its newest own post.
export function parseUserTweetsPayload(body, { ownId = '', username }) {
  const posts = [];
  let oldestEntry = Infinity;
  let reachedEnd = false;
  let hasBottomCursor = false;
  let hasTweetOrModuleEntry = false;

  for (const instruction of timelineInstructions(body)) {
    if (instruction.type === 'TimelineTerminateTimeline' && instruction.direction === 'Bottom') {
      reachedEnd = true;
      continue;
    }
    if (instruction.type === 'TimelinePinEntry') {
      const parsed = parseTweet(tweetResult(instruction.entry?.content?.itemContent), ownId, username);
      if (parsed?.post) posts.push(parsed.post);
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
        const parsed = parseTweet(tweetResult(content.itemContent), ownId, username);
        if (parsed?.post) posts.push(parsed.post);
        if (parsed) oldestEntry = Math.min(oldestEntry, parsed.time);
        continue;
      }

      if (content?.entryType === 'TimelineTimelineModule') {
        hasTweetOrModuleEntry = true;
        let newest = -Infinity;
        for (const moduleItem of content.items || []) {
          const parsed = parseTweet(tweetResult(moduleItem?.item?.itemContent), ownId, username);
          if (parsed?.post) posts.push(parsed.post);
          if (parsed) newest = Math.max(newest, parsed.time);
        }
        if (newest > -Infinity) oldestEntry = Math.min(oldestEntry, newest);
      }
    }
  }
  if (hasBottomCursor && !hasTweetOrModuleEntry) reachedEnd = true;

  return { posts, oldestEntry, reachedEnd };
}

// X lists headline posts on the Posts tab and link replies on the Replies tab. The older
// UserTweetsAndReplies also lists the posts; reading both tabs covers either layout.
const PROFILE_TABS = new Map([
  ['UserTweets', 'posts'],
  ['UserOriginalsTimeline', 'posts'],
  ['UserTweetsAndReplies', 'replies'],
  ['UserRepliesTimeline', 'replies'],
]);

// Returns 'posts', 'replies' or '' for other responses.
export function profileTab(responseURL) {
  try {
    return PROFILE_TABS.get(new URL(responseURL).pathname.split('/').pop()) || '';
  } catch {
    return '';
  }
}

export function isFirstUserTweetsPage(responseURL) {
  try {
    const raw = new URL(responseURL).searchParams.get('variables');
    if (!raw) return false;
    const variables = JSON.parse(raw);
    return variables !== null
      && typeof variables === 'object'
      && !Array.isArray(variables)
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
      if (page.oldestEntry < cutoff || page.reachedEnd) reachedBoundary = true;
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

// With inReplyTo, a reply that X reports under another post is rejected.
export function parseCreateTweetResponse(body, username, { inReplyTo } = {}) {
  const result = body?.data?.create_tweet?.tweet_results?.result;
  const id = result?.rest_id;
  if (!id) {
    const reason = errorMessages(body) || 'no tweet id in response';
    throw new Error(`X rejected the post: ${reason}`);
  }
  const parent = result.legacy?.in_reply_to_status_id_str;
  if (inReplyTo && parent !== inReplyTo) {
    throw new Error(`X published the reply under ${parent ? `post ${parent}` : 'no post'} instead of ${inReplyTo}`);
  }

  return { id, url: `https://x.com/${screenName(result) || username}/status/${id}` };
}

// Preserve the error and click state so the orchestrator can reconcile uncertain outcomes.
export function postErrorResponse(error) {
  return {
    error: error.message, clicked: Boolean(error.clicked), stage: error.stage || 'service',
    ...(error.captions && { captions: error.captions }),
    ...(error.fallbackReason && { fallbackReason: error.fallbackReason }),
  };
}
