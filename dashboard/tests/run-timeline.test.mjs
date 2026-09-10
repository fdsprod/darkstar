import { test } from 'node:test';
import assert from 'node:assert/strict';
import { selectTimelineRange, timelineRange } from '../src/components/terminal/timelineModel.ts';

test('full-width selections restore the entire live run', () => {
  const selection = selectTimelineRange([100, 200], 100, 200);
  assert.deepEqual(selection, {kind:'all'});
  assert.equal(timelineRange(selection, 300), null);
});
test('a narrowed range at the right edge includes new events and keeps its start', () => {
  const selection = selectTimelineRange([150, 200], 100, 200);
  assert.deepEqual(selection, {kind:'live', start:150});
  assert.deepEqual(timelineRange(selection, 350), [150, 350]);
});
test('historical selections remain fixed as the run grows', () => {
  const selection = selectTimelineRange([120, 190], 100, 200);
  assert.deepEqual(timelineRange(selection, 350), [120, 190]);
  assert.deepEqual(selectTimelineRange(null, 100, 350), {kind:'all'});
});
