import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const unitMs = { Minute: 60_000, Hour: 3_600_000 };

// Reads a constant from its one declaration, so a renamed or duplicated declaration fails
// instead of being skipped. Only products and left shifts of integers are evaluated.
function declared(file, name) {
  const source = readFileSync(new URL(`../${file}`, import.meta.url), 'utf8');
  const pattern = new RegExp(`^\\s*(?:export )?(?:const )?${name}\\s*=\\s*([\\d_ *<]+?)(?: \\* time\\.(Minute|Hour))?;?$`, 'gm');
  const matches = [...source.matchAll(pattern)];
  assert.equal(matches.length, 1, `${file} must declare ${name} once`);
  const [, expression, unit] = matches[0];
  const value = expression.split('*').reduce((product, factor) => {
    const [base, shift = '0'] = factor.replaceAll('_', '').split('<<');
    return product * Number(base) * 2 ** Number(shift);
  }, 1);
  assert.ok(Number.isSafeInteger(value) && value > 0, `${file} declares ${name} as ${expression}`);
  return unit ? value * unitMs[unit] : value;
}

test('the orchestrator and the poster agree on budgets and limits', () => {
  const postTimeout = declared('internal/poster/client.go', 'postTimeout');
  const postBudget = declared('poster/server.mjs', 'POST_TIMEOUT_MS');
  assert.ok(postBudget < postTimeout, `POST_TIMEOUT_MS ${postBudget} must stay below postTimeout ${postTimeout}`);

  const videoTimeout = declared('internal/poster/client.go', 'videoPostTimeout');
  const videoBudget = declared('poster/media.mjs', 'VIDEO_RECEIVE_TIMEOUT_MS') + declared('poster/server.mjs', 'VIDEO_POST_TIMEOUT_MS');
  assert.ok(videoBudget < videoTimeout, `video receipt plus work ${videoBudget} must stay below videoPostTimeout ${videoTimeout}`);

  assert.deepEqual({
    recentHours: declared('poster/server.mjs', 'MAX_RECENT_HOURS'),
    videoBytes: declared('poster/media.mjs', 'MAX_VIDEO_BYTES'),
    videoMs: declared('poster/subtitles.mjs', 'MAX_VIDEO_MS'),
  }, {
    recentHours: declared('internal/poster/client.go', 'MaxLookbackHours'),
    videoBytes: declared('internal/article/video.go', 'maxVideoSize'),
    videoMs: declared('internal/article/mp4.go', 'maxVideoDuration'),
  });
});
