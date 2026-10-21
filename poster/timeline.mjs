function errorMessages(body) {
  return body?.errors?.map((error) => error?.message).filter(Boolean).join('; ');
}

// A present handle is used as is, so a malformed one cannot hide behind the legacy fallback.
function screenName(result) {
  const user = result?.core?.user_results?.result;
  return user?.core?.screen_name ?? user?.legacy?.screen_name;
}

function timelineInstructions(body) {
  if (body && Object.hasOwn(body, 'errors') && !Array.isArray(body.errors)) {
    throw new Error('UserTweets returned malformed errors');
  }
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
  if (itemContent?.itemType === 'TimelineTweet' && !Object.hasOwn(itemContent, 'tweet_results')) {
    throw new Error('UserTweets has a tweet item without tweet_results');
  }
  let result = itemContent?.tweet_results?.result;
  if (result?.__typename === 'TweetWithVisibilityResults') {
    if (!result.tweet || typeof result.tweet !== 'object' || Array.isArray(result.tweet)) {
      throw new Error('UserTweets has a visibility wrapper without a tweet');
    }
    result = result.tweet;
  }
  // An unknown wrapper, also inside a visibility wrapper, would hide its post like a tombstone.
  if (result && Object.hasOwn(result, 'tweet')) throw new Error(`UserTweets has an unknown post wrapper: ${result.__typename}`);
  return result;
}

// X omits empty entity lists. A missing urls list means no links only when every t.co link
// in the text is a media link; otherwise it stays undefined and the post is unreadable.
function entityUrls(text, entities) {
  if (entities?.urls !== undefined) return entities.urls;
  if (typeof entities !== 'object' || entities === null || Array.isArray(entities)) return undefined;
  const media = entities.media === undefined ? [] : entities.media;
  if (!Array.isArray(media) || typeof text !== 'string') return undefined;
  const mediaLinks = new Set(media.map((item) => item?.url));
  const links = text.match(/https:\/\/t\.co\/\w+/g) || [];
  return links.every((link) => mediaLinks.has(link)) ? [] : undefined;
}

// Unreadable link entities return null so duplicate checks fail closed.
// Media links need no expansion; article links do.
function expandedUrls(legacy, note) {
  const lists = [
    entityUrls(legacy?.full_text, legacy?.entities),
    note ? entityUrls(note.text, { media: legacy?.entities?.media, ...note.entity_set }) : [],
  ];
  if (!lists.every(Array.isArray)) return null;
  const urls = lists.flat().map((url) => url?.expanded_url);
  if (!urls.every((url) => typeof url === 'string' && url.trim() !== '')) return null;
  return [...new Set(urls)];
}

// X sends IDs as decimal strings; numbers would lose precision and arrays only coerce.
const isDecimalID = (value) => typeof value === 'string' && /^\d+$/.test(value);
// X handles are 1-15 letters, digits or underscores; anything else would break a status URL.
const isHandle = (value) => typeof value === 'string' && /^\w{1,15}$/.test(value);

// Returns 'own' or 'foreign', undefined for an unknown author, or null for a malformed one,
// which must not decide authorship either way.
function author(userId, handle, ownId, username) {
  if ((userId != null && !isDecimalID(userId)) || (handle != null && !isHandle(handle))) return null;
  if ((ownId && userId === ownId) || handle?.toLowerCase() === username.toLowerCase()) return 'own';
  if ((ownId && userId) || handle) return 'foreign';
  return undefined;
}

// Self-replies keep their parent's URL; replies to others are not posts of the account.
// An unknown parent author could hide a link reply, so it fails closed.
function replyParent(legacy, ownId, username, handle) {
  const parentId = legacy.in_reply_to_status_id_str;
  const parentHandle = legacy.in_reply_to_screen_name;
  const parent = author(legacy.in_reply_to_user_id_str, parentHandle, ownId, username);
  if (parentId === undefined || parentId === null) {
    // An own mention-led post can have a reply target without being a reply.
    const mention = /^@(\w+)/.exec(legacy.full_text)?.[1];
    return parent === null || (parent === 'own' && (!isHandle(mention) || mention.toLowerCase() !== username.toLowerCase()))
      ? { unreadable: true } : {};
  }
  // A malformed parent ID would attach the reply to the wrong post.
  if (!isDecimalID(parentId) || !parent) return { unreadable: true };
  if (parent === 'foreign') return { foreign: true };
  return { replyTo: `https://x.com/${parentHandle || handle || username}/status/${parentId}` };
}

// Returns null for others' posts, retweets and tombstones. Otherwise time is the post's timeline
// position, and post is unset for own replies to others.
function parseTweet(result, ownId, username) {
  const legacy = result?.legacy;
  const handle = screenName(result);
  const by = author(legacy?.user_id_str, handle, ownId, username);
  // An unknown author could hide a duplicate; tombstones and non-tweets are skipped.
  if (by === null || (by === undefined && (result?.rest_id || legacy))) {
    throw new Error(`UserTweets has a post without a readable author: ${result.rest_id || 'no id'}`);
  }
  if (by !== 'own') return null;
  const retweet = legacy?.retweeted_status_result;
  if (retweet && typeof retweet === 'object' && !Array.isArray(retweet)) return null;

  // Unreadable own posts could hide duplicates, even beyond the cutoff.
  const createdAt = CREATED_AT.test(legacy?.created_at) ? new Date(legacy.created_at) : new Date(NaN);
  const note = result.note_tweet?.note_tweet_results?.result;
  const urls = expandedUrls(legacy, note);
  const parent = legacy ? replyParent(legacy, ownId, username, handle) : {};
  if (!isDecimalID(result.rest_id) || retweet || typeof legacy?.full_text !== 'string' || Number.isNaN(createdAt.getTime()) || !urls || parent.unreadable) {
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

// Unknown types could hide a post, so failing closed is required.
const ENTRY_TYPES = new Set(['TimelineTimelineCursor', 'TimelineTimelineItem', 'TimelineTimelineModule']);
const INSTRUCTION_TYPES = new Set([
  'TimelineAddEntries', 'TimelineReplaceEntry', 'TimelinePinEntry', 'TimelineTerminateTimeline',
  'TimelineClearCache', 'TimelineShowAlert', 'TimelineClearEntriesUnreadState',
  'TimelineMarkEntriesUnreadGreaterThanSortIndex', 'TimelineShowCover',
]);

// Search pinned posts and conversation modules, but skip quotes and retweets.
// Unpinned entries prove the cutoff; a conversation module sorts by its newest own post.
export function parseUserTweetsPayload(body, { ownId = '', username }) {
  const posts = [];
  let oldestEntry = Infinity;
  let reachedEnd = false;
  let hasBottomCursor = false;
  let hasTweetOrModuleEntry = false;

  for (const instruction of timelineInstructions(body)) {
    if (!INSTRUCTION_TYPES.has(instruction.type)) {
      throw new Error(`UserTweets has an unknown instruction type: ${instruction.type}`);
    }
    if (instruction.type === 'TimelineTerminateTimeline' && instruction.direction === 'Bottom') {
      reachedEnd = true;
      continue;
    }
    if (instruction.type === 'TimelinePinEntry') {
      const itemContent = instruction.entry?.content?.itemContent;
      if (!itemContent) throw new Error('UserTweets has a pinned post without item content');
      const parsed = parseTweet(tweetResult(itemContent), ownId, username);
      if (parsed?.post) posts.push(parsed.post);
      continue;
    }

    let entries = [];
    if (instruction.type === 'TimelineAddEntries') {
      if (!Array.isArray(instruction.entries)) throw new Error('UserTweets has malformed entries');
      entries = instruction.entries;
    } else if (instruction.type === 'TimelineReplaceEntry') {
      entries = instruction.entry ? [instruction.entry] : [];
    }

    for (const entry of entries) {
      const content = entry?.content;
      if (content && !ENTRY_TYPES.has(content.entryType)) {
        throw new Error(`UserTweets has an unknown entry type: ${content.entryType}`);
      }
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
  if (ownId && !isDecimalID(ownId)) throw new Error('UserTweets has a malformed own user id');
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
  if (!isDecimalID(id)) throw new Error('X returned a malformed post id');
  const parent = result.legacy?.in_reply_to_status_id_str;
  if (inReplyTo && parent !== inReplyTo) {
    throw new Error(`X published the reply under ${parent ? `post ${parent}` : 'no post'} instead of ${inReplyTo}`);
  }

  const handle = screenName(result);
  return { id, url: `https://x.com/${isHandle(handle) ? handle : username}/status/${id}` };
}

// Preserve the error and click state so the orchestrator can reconcile uncertain outcomes.
export function postErrorResponse(error) {
  return {
    error: error.message, clicked: Boolean(error.clicked), stage: error.stage || 'service',
    ...(error.captions && { captions: error.captions }),
    ...(error.fallbackReason && { fallbackReason: error.fallbackReason }),
  };
}
