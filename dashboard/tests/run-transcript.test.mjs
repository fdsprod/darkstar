import test from 'node:test';
import assert from 'node:assert/strict';
import { buildTranscript, inTimeRange } from '../src/pages/runTranscriptModel.ts';

function event(position, method, params, subject = 'attempt-a') { return { position, time: new Date(1700000000000 + position * 1000).toISOString(), kind: 'attempt.provider_event', subject, data: { payload: { providerMethod: method, params } } }; }
test('replay joins deltas and replaces completion snapshots without duplicating output', () => {
 const events = [event(1, 'turn/started', { turn: { id: 't1' } }), event(2, 'item/started', { item: { id: 'cmd', type: 'commandExecution', command: 'go test ./...' } }), event(3, 'item/commandExecution/outputDelta', { itemId: 'cmd', delta: 'PASS\n' }), event(4, 'item/completed', { item: { id: 'cmd', type: 'commandExecution', aggregatedOutput: 'PASS\n', exitCode: 0 } }), event(5, 'item/agentMessage/delta', { itemId: 'msg', delta: 'Done.' }), event(6, 'item/completed', { item: { id: 'msg', type: 'agentMessage', text: 'Done.' } })];
 const result = buildTranscript(events);
 assert.equal(result.turns.length, 2); assert.equal(result.entries.length, 2); assert.equal(result.entries[0].output, 'PASS\n'); assert.equal(result.entries[1].text, 'Done.'); assert.equal(result.entries[0].raw.length, 3);
 assert.equal(inTimeRange(result.entries[0], [Date.parse(events[2].time), Date.parse(events[2].time)]), true);
 assert.equal(inTimeRange(result.entries[0], [0, 10]), false);
});
test('usage uses the latest cumulative value per thread; missing counts stay unknown', () => {
 const result = buildTranscript([event(1, 'thread/tokenUsage/updated', { threadId: 'one', tokenUsage: { total: { inputTokens: 100, outputTokens: 20 } } }), event(2, 'thread/tokenUsage/updated', { threadId: 'one', tokenUsage: { total: { inputTokens: 200, outputTokens: 30 } } }), event(3, 'thread/tokenUsage/updated', { threadId: 'two', tokenUsage: { total: { inputTokens: 50, outputTokens: 10 } } }, 'attempt-b')]);
 assert.deepEqual(result.usage, { input: 250, output: 40 }); assert.equal(result.usage.reasoning, undefined);
});
test('only provider-exposed reasoning summaries are rendered; unrecognized events remain in original history', () => {
 const result = buildTranscript([event(1, 'item/started', { item: { id: 'r', type: 'reasoning', content: ['not a summary'] } }), event(2, 'item/reasoning/summaryTextDelta', { itemId: 'r', delta: 'Checking the tests.' }), event(3, 'item/reasoning/textDelta', { itemId: 'r', delta: 'not a summary' }), { position: 4, time: new Date().toISOString(), kind: 'attempt.provider_event', subject: 'attempt-a', data: { historyGap: true } }]);
 assert.equal(result.entries[0].text, 'Checking the tests.'); assert.equal(result.gaps, 1);
});
test('legacy tool calls recover input and saved dynamic results remain readable', () => {
 const first = event(1, 'item/tool/call', { callId: 'call', tool: 'submit_output', arguments: { id: 'plan', value: '# Plan' } });
 first.data.payload.method = first.data.payload.providerMethod; delete first.data.payload.providerMethod;
 const result = buildTranscript([first, event(2, 'item/completed', { item: { id: 'call', type: 'dynamicToolCall', tool: 'submit_output', output: { contentItems: [{ type: 'inputText', text: '{"status":"recorded"}' }] } } })]);
 assert.equal(result.entries.length, 1); assert.match(result.entries[0].text, /# Plan/); assert.match(result.entries[0].output, /recorded/);
});
