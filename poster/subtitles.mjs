import { openAsBlob } from 'node:fs';
import { TextDecoder } from 'node:util';

const SUBTITLE_TIMEOUT_MS = 10 * 60_000;
const MAX_RESPONSE_BYTES = 4 * 1024 * 1024;
// Must match maxVideoDuration in internal/article/mp4.go.
const MAX_VIDEO_MS = 20 * 60_000;
const SRT_CUE = /^(\d+)\n(\d{2}:[0-5]\d:[0-5]\d,\d{3}) --> (\d{2}:[0-5]\d:[0-5]\d,\d{3})\n\S[^]*$/;
const utf8 = new TextDecoder('utf-8', { fatal: true });

// SRT timestamp (HH:MM:SS,mmm) in milliseconds.
function milliseconds(time) {
  const [hours, minutes, seconds, ms] = time.split(/[:,]/).map(Number);
  return ((hours * 60 + minutes) * 60 + seconds) * 1000 + ms;
}

// The 10-minute timeout always applies; signal adds caller cancellation.
export async function generateSubtitles(videoFile, {
  apiKey = process.env.ELEVENLABS_API_KEY,
  signal,
} = {}) {
  if (!apiKey) throw new Error('ELEVENLABS_API_KEY is required for video subtitles');
  const form = new FormData();
  form.set('model_id', 'scribe_v2');
  form.set('language_code', 'nld');
  form.set('tag_audio_events', 'false');
  // ElevenLabs requires diarization and word timings for additional export formats.
  form.set('diarize', 'true');
  form.set('timestamps_granularity', 'word');
  form.set('additional_formats', JSON.stringify([{
    format: 'srt', include_speakers: false, max_characters_per_line: 42,
    max_segment_chars: 84, max_segment_duration_s: 6,
  }]));
  // Stream from disk to keep the MP4 out of the JS heap.
  form.set('file', await openAsBlob(videoFile, { type: 'video/mp4' }), 'video.mp4');
  const timeout = AbortSignal.timeout(SUBTITLE_TIMEOUT_MS);
  const response = await fetch('https://api.elevenlabs.io/v1/speech-to-text', {
    method: 'POST', headers: { 'xi-api-key': apiKey }, body: form, redirect: 'error',
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  if (!response.ok) {
    await response.body?.cancel();
    throw new Error(`ElevenLabs transcription returned HTTP ${response.status}`);
  }
  const chunks = [];
  let bytes = 0;
  for await (const chunk of response.body) {
    bytes += chunk.length;
    if (bytes > MAX_RESPONSE_BYTES) throw new Error('ElevenLabs transcript is too large');
    chunks.push(chunk);
  }
  const transcript = JSON.parse(utf8.decode(Buffer.concat(chunks)));
  const output = transcript.additional_formats?.find((format) => format.requested_format === 'srt');
  if (!output || typeof output.content !== 'string') throw new Error('ElevenLabs returned no SRT captions');
  const srt = output.is_base64_encoded
    ? utf8.decode(Buffer.from(output.content, 'base64'))
    : output.content;
  // Reject empty transcripts, malformed timings and incomplete cues before X sees them.
  const cues = srt.trim().replaceAll('\r\n', '\n').split('\n\n');
  let previousEnd = 0;
  for (const [index, cue] of cues.entries()) {
    const match = cue.match(SRT_CUE);
    if (!match || Number(match[1]) !== index + 1) throw new Error('ElevenLabs returned invalid SRT captions');
    const start = milliseconds(match[2]);
    const end = milliseconds(match[3]);
    if (start < previousEnd || end <= start || end > MAX_VIDEO_MS) throw new Error('ElevenLabs returned invalid SRT timing');
    previousEnd = end;
  }
  return srt;
}
