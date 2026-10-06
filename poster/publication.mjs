import { prepareSubtitles } from './subtitles.mjs';

// clicked is kept for the Go contract: true means CreateTweet may have been sent.
// Neither this function nor the transport retries a publication request.
export async function publish(client, { text, image, dryRun, skipCaptions }, signal, videoFile, transcribe = prepareSubtitles) {
  signal.throwIfAborted();
  if (dryRun) return { dryRun: true, ...(videoFile ? { captions: 'unverified' } : {}) };
  let clicked = false;
  let caption = {};
  try {
    await client.ensureSession(signal);
    let srt;
    if (videoFile) ({ srt, ...caption } = await transcribe(videoFile, { signal, skip: skipCaptions }));
    signal.throwIfAborted();
    let media;
    if (videoFile) {
      media = await client.uploadVideo(videoFile, signal);
      if (srt) {
        try { await client.attachSubtitles(media, srt, signal); }
        catch (error) {
          signal.throwIfAborted();
          if (error.stage !== 'captions') throw error;
          caption = { captions: 'none', fallbackReason: `caption attachment: ${error.message}` };
          // Use a clean video if an association failed, just as composer recovery did.
          media = await client.uploadVideo(videoFile, signal);
        }
      }
    } else if (image) media = await client.uploadImage(image, signal);
    signal.throwIfAborted();
    clicked = true;
    return { ...await client.createPost(text, media, signal), ...caption };
  } catch (error) {
    throw Object.assign(error, { clicked, ...caption });
  }
}
