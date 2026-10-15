import assert from 'node:assert/strict';
import test from 'node:test';

import { pacingScale } from './server.mjs';

test('human pacing stays on unless a scale from 0 to 1 is set', () => {
  assert.equal(pacingScale(undefined), 1);
  // An empty variable in an env file must not switch pacing off.
  assert.equal(pacingScale(''), 1);
  assert.equal(pacingScale('0'), 0);
  assert.equal(pacingScale('0.5'), 0.5);
  // Whitespace and other non-decimal text must not read as 0.
  for (const value of ['abc', '-1', '2', 'Infinity', ' ', '\t', ' 0', '1e-1', '0x0']) {
    assert.throws(() => pacingScale(value), /POSTER_PACING must be a number from 0 to 1/, value);
  }
});
