import assert from 'node:assert/strict';
import test from 'node:test';
import xUI from './x-ui.json' with { type: 'json' };

test('media upload banners match both languages with or without a period', () => {
  const pattern = new RegExp(xUI.mediaUploadFailedPattern, 'i');
  for (const message of ['Some of your media failed to upload', 'Een deel van je media kon niet worden geüpload']) {
    for (const ending of ['', '.']) {
      assert.equal(pattern.test(message + ending), true);
      assert.equal(pattern.test((message + ending).toUpperCase()), true);
    }
    assert.equal(pattern.test(`Prefix ${message}`), false);
    assert.equal(pattern.test(`${message} suffix`), false);
    assert.equal(pattern.test(`${message}..`), false);
  }
  assert.equal(pattern.test(''), false);
});

test('the post menu Delete item matches both languages exactly', () => {
  const pattern = new RegExp(xUI.deleteMenuPattern, 'i');
  for (const label of ['Delete', 'Verwijderen']) assert.equal(pattern.test(label), true);
  for (const label of ['Delete post?', 'Delete all', 'Bladwijzer verwijderen', '']) assert.equal(pattern.test(label), false);
});
