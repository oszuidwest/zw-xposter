import { openAsBlob } from 'node:fs';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { TextDecoder } from 'node:util';

export const SUBTITLE_TIMEOUT_MS = 10 * 60_000;
const MAX_RESPONSE_BYTES = 4 * 1024 * 1024;

export async function generateSubtitles(videoFile, {
  apiKey = process.env.ELEVENLABS_API_KEY,
  signal = AbortSignal.timeout(SUBTITLE_TIMEOUT_MS),
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
  // Native file-backed Blob keeps even a 512 MiB MP4 out of the JS heap.
  form.set('file', await openAsBlob(videoFile, { type: 'video/mp4' }), 'video.mp4');
  const response = await fetch('https://api.elevenlabs.io/v1/speech-to-text', {
    method: 'POST', headers: { 'xi-api-key': apiKey }, body: form, signal, redirect: 'error',
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
  const transcript = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks)));
  const output = transcript.additional_formats?.find((format) => format.requested_format === 'srt');
  if (!output || typeof output.content !== 'string') throw new Error('ElevenLabs returned no SRT captions');
  const srt = output.is_base64_encoded
    ? new TextDecoder('utf-8', { fatal: true }).decode(Buffer.from(output.content, 'base64'))
    : output.content;
  // Reject empty transcripts, malformed timings and incomplete cues before X sees them.
  const cues = srt.trim().replaceAll('\r\n', '\n').split('\n\n');
  let previousEnd = 0;
  for (const [index, cue] of cues.entries()) {
    const match = cue.match(/^(\d+)\n(\d{2}:[0-5]\d:[0-5]\d,\d{3}) --> (\d{2}:[0-5]\d:[0-5]\d,\d{3})\n(\S[^]*)$/);
    if (!match || Number(match[1]) !== index + 1) throw new Error('ElevenLabs returned invalid SRT captions');
    const milliseconds = (time) => time.split(/[:,]/).reduce((total, part, i) => total * (i === 3 ? 1000 : 60) + Number(part), 0);
    const start = milliseconds(match[2]);
    const end = milliseconds(match[3]);
    if (start < previousEnd || end <= start || end > 20 * 60_000) throw new Error('ElevenLabs returned invalid SRT timing');
    previousEnd = end;
  }
  const file = path.join(path.dirname(videoFile), 'video.nl.srt');
  await writeFile(file, srt, { mode: 0o600, flag: 'wx' });
  return file;
}
