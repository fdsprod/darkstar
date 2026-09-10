import assert from 'node:assert/strict';
import test from 'node:test';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { readFile, writeFile, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { transform } from 'esbuild';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const sdk = await readFile(resolve(root, 'packages/plugin-sdk/src/provider.ts'), 'utf8');
const source = (await readFile(resolve(root, 'plugins/provider-codex/src/index.ts'), 'utf8')).replace(/^import .*from '\.\.\/\.\.\/\.\.\/packages\/plugin-sdk\/src\/provider';\r?\n/m, '');
const { code } = await transform(sdk + '\n' + source, { loader: 'ts', format: 'esm', target: 'node20' });
const fixture = resolve(root, 'plugins/provider-codex/tests/app-server-fixture.mjs');

async function session(t, mode = 'success') {
  const dir = await mkdtemp(resolve(tmpdir(), 'darkstar-ts-provider-'));
  const bundle = resolve(dir, 'provider.mjs'); await writeFile(bundle, code);
  const child = spawn(process.execPath, [bundle], { stdio: 'pipe', windowsHide: true });
  let stderr = ''; child.stderr.on('data', b => { stderr += b; });
  let sequence = 0; const pending = new Map(); const calls = []; const evidence = [];
  const lines = createInterface({ input: child.stdout });
  const send = value => child.stdin.write(JSON.stringify(value) + '\n');
  lines.on('line', line => {
    const response = JSON.parse(line);
    if (response.type === 'host_call') {
      calls.push(response);
      let result;
      if (response.method === 'evidence.record') { evidence.push(response.params); result = { Kind: 'provider', Ref: `evidence:${evidence.length}`, Digest: 'a'.repeat(64) }; }
      else if (response.method === 'tool.call') result = { recorded: true };
      else if (response.method === 'outputs.resolve') result = { output: 'durable submission' };
      else if (response.method === 'markdown.capture') result = {};
      else throw new Error('Unknown host call');
      send({ type: 'host_result', id: response.id, result }); return;
    }
    const waiter = pending.get(response.id); pending.delete(response.id);
    response.error ? waiter.reject(new Error(response.error)) : waiter.resolve(response.result);
  });
  function call(method, params = {}) { const id = `request-${++sequence}`; return new Promise((resolve, reject) => { pending.set(id, { resolve, reject }); send({ type: 'request', id, method, params }); }); }
  t.after(async () => { await call('provider.shutdown'); child.stdin.end(); await new Promise(done => child.once('exit', done)); assert.equal(stderr, ''); await rm(dir, { recursive: true, force: true }); });
  await call('provider.configure', { Executable: process.execPath, Arguments: [fixture, mode], ProjectRoot: dir });
  const request = { AttemptID: 'attempt-1', RunID: 'run-1', NodeID: 'node-1', IdempotencyKey: 'start-1', Workspace: dir,
    Access: 'read_only', Network: 'denied', CommandPolicy: 'ask', FilePolicy: 'ask', ToolPolicy: 'ask', Prompt: 'Scoped task only',
    OutputSchema: { type: 'object' }, DynamicTools: [], Inputs: [], Timeout: 0, CancellationGrace: 1e9 };
  return { call, request, calls, evidence };
}

test('TypeScript connector owns RPC mapping, normalization, evidence, usage, result and idempotent start', async t => {
  const s = await session(t);
  const [handle, retry] = await Promise.all([s.call('provider.start', s.request), s.call('provider.start', s.request)]);
  assert.deepEqual(handle, retry);
  const result = await s.call('provider.result', { Handle: handle });
  assert.equal(result.Kind, 'succeeded'); assert.deepEqual(result.StructuredOutput, { answer: 42 });
  assert.equal(result.Usage.InputTokens, 11);
  const batch = await s.call('provider.events', { Handle: handle, AfterSequence: 0 });
  assert.equal(batch.Terminal, true);
  assert.deepEqual(batch.Events.map(e => e.Sequence), batch.Events.map((_, i) => i + 1));
  assert.ok(batch.Events.some(e => e.Kind === 'unknown.provider_event'));
  assert.ok(s.evidence.some(e => e.Data.method === 'future/event'));
  await assert.rejects(s.call('provider.start', { ...s.request, Prompt: 'different' }), /conflicts/);
});

test('health excludes account identity and capability contracts are explicit', async t => {
  const s = await session(t);
  const health = await s.call('provider.health'); assert.equal(health.State, 'available');
  assert.equal(health.Authentication, 'authenticated'); assert.equal(health.Usage, 'ready');
  assert.ok(!JSON.stringify(health).includes('secret@example.com'));
  assert.equal((await s.call('provider.capabilities')).Features.resume.Kind, 'available');
});

test('dynamic tools execute only through host and completed output comes from durable submissions', async t => {
  const s = await session(t, 'tool');
  s.request.ToolBackedOutputs = true; s.request.DynamicTools = [{ type: 'function', name: 'journal_items', description: 'Read', inputSchema: { type: 'object' } }];
  const handle = await s.call('provider.start', s.request);
  const result = await s.call('provider.result', { Handle: handle });
  assert.deepEqual(result.StructuredOutput, { output: 'durable submission' });
  assert.deepEqual(s.calls.find(c => c.method === 'tool.call').params, { AttemptID: 'attempt-1', CallID: 'call-1', Name: 'journal_items', Arguments: { operation: 'read' } });
});

test('permission decisions are checkpoint-bound and translated only after host response', async t => {
  const s = await session(t, 'approval'); const handle = await s.call('provider.start', s.request);
  let events = [];
  while (!events.some(e => e.Kind === 'permission.requested')) {
    const batch = await s.call('provider.events', { Handle: handle, AfterSequence: events.at(-1)?.Sequence ?? 0 }); events.push(...batch.Events);
  }
  const cp = events.find(e => e.Kind === 'permission.requested').Payload.checkpoint;
  const response = { AttemptID: 'attempt-1', ProviderThreadID: 'thread-1', ProviderRequestID: cp.providerRequestId, IdempotencyKey: 'response-1', ScopeDigest: cp.scopeDigest, Decision: 'allow_once' };
  await assert.rejects(s.call('provider.respond', { ...response, ScopeDigest: 'wrong' }), /scope mismatch/);
  await s.call('provider.respond', response);
  assert.equal((await s.call('provider.result', { Handle: handle })).Kind, 'succeeded');
  await s.call('provider.respond', response); // Safe exact retry, even after request resolution.
});

test('cancellation interrupts and confirms process exit', async t => {
  const s = await session(t, 'wait'); const handle = await s.call('provider.start', s.request);
  const result = await s.call('provider.cancel', { Handle: handle, IdempotencyKey: 'cancel-1', GracePeriod: 1e9 });
  assert.equal(result.Disposition, 'graceful'); assert.equal((await s.call('provider.result', { Handle: handle })).Kind, 'cancelled');
});

test('resume refuses a completed recorded turn; network policy and version fail closed', async t => {
  const s = await session(t, 'stale-resume');
  await assert.rejects(s.call('provider.resume', { AttemptID: 'resume-1', IdempotencyKey: 'resume', ProviderThreadID: 'thread-1', ProviderTurnID: 'turn-1', ContextDigest: 'a'.repeat(64), WorkspaceDigest: 'b'.repeat(64), LastSequence: 4 }), /not active/);
  await assert.rejects(s.call('provider.start', { ...s.request, Network: 'allowed' }), /policy/);
});

test('a foreign attempt event cannot produce success', async t => {
  const s = await session(t, 'wrong-scope'); const handle = await s.call('provider.start', s.request);
  assert.equal((await s.call('provider.result', { Handle: handle })).Kind, 'unknown');
});

test('successful resume rejoins the recorded turn and continues sequence without starting another turn', async t => {
  const s = await session(t, 'resume');
  const handle = await s.call('provider.resume', { AttemptID: 'attempt-1', IdempotencyKey: 'resume', ProviderThreadID: 'thread-1', ProviderTurnID: 'turn-1', ContextDigest: 'a'.repeat(64), WorkspaceDigest: 'b'.repeat(64), LastSequence: 4 });
  assert.equal((await s.call('provider.result', { Handle: handle })).Kind, 'succeeded');
  const batch = await s.call('provider.events', { Handle: handle, AfterSequence: 4 });
  assert.equal(batch.Events[0].Sequence, 5);
  assert.ok(!batch.Events.some(e => e.Kind === 'attempt.started'));
});

test('unreviewed Codex version is unavailable and cannot start an attempt', async t => {
  const s = await session(t, 'bad-version');
  assert.equal((await s.call('provider.health')).State, 'unavailable');
  await assert.rejects(s.call('provider.start', s.request), /Unsupported/);
});

test('timeout interrupts the provider and reports failure rather than success', async t => {
  const s = await session(t, 'wait'); s.request.Timeout = 5e7;
  const handle = await s.call('provider.start', s.request);
  const result = await s.call('provider.result', { Handle: handle });
  assert.equal(result.Kind, 'failed'); assert.equal(result.Failure.Code, 'timeout');
});

test('bounded event batches retain every event after terminal completion', async t => {
  const s = await session(t, 'many-events'); const handle = await s.call('provider.start', s.request);
  await s.call('provider.result', { Handle: handle });
  const events = []; let terminal = false;
  while (!terminal) {
    const batch = await s.call('provider.events', { Handle: handle, AfterSequence: events.at(-1)?.Sequence ?? 0 });
    assert.ok(batch.Events.length <= 128); events.push(...batch.Events); terminal = batch.Terminal;
  }
  assert.equal(events.filter(e => e.Kind === 'message.delta').length, 300);
  assert.deepEqual(events.map(e => e.Sequence), events.map((_, i) => i + 1));
});
