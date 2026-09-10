import { createInterface } from 'node:readline';
const mode = process.argv[2] ?? 'success';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const notify = (method, params) => send({ method, params });
const scope = { threadId: 'thread-1', turnId: 'turn-1' };
let threadParams;
function finish() {
  if (mode === 'many-events') for (let i = 0; i < 300; i++) notify('item/agentMessage/delta', { ...scope, delta: 'a', itemId: 'message-1' });
  notify('thread/tokenUsage/updated', { ...scope, tokenUsage: { total: { inputTokens: 11, cachedInputTokens: 4, outputTokens: 7 } } });
  notify('item/completed', { ...scope, item: { id: 'message-1', type: 'agentMessage', phase: 'final_answer', text: JSON.stringify({ answer: 42 }) } });
  notify('future/event', { ...scope, text: 'retained evidence' });
  notify('turn/completed', { threadId: 'thread-1', turn: { id: 'turn-1', status: 'completed' } });
}
const lines = createInterface({ input: process.stdin });
lines.on('close', () => process.exit(0));
lines.on('line', line => {
  const r = JSON.parse(line);
  if (r.id === 'approval-1' && !r.method) {
    if (r.result.decision !== 'accept') throw new Error('Wrong decision translation');
    notify('serverRequest/resolved', { ...scope, requestId: r.id }); finish(); return;
  }
  if (r.id === 'tool-1' && !r.method) {
    if (!r.result.success || r.result.contentItems[0].text !== '{"recorded":true}') throw new Error('Wrong tool translation');
    finish(); return;
  }
  if (!r.method || r.id === undefined) return;
  let result = {};
  switch (r.method) {
    case 'initialize': result = { userAgent: `Codex/${mode === 'bad-version' ? '9.9.9' : '0.153.4'}`, codexHome: '/private/home', platformFamily: 'windows', platformOs: 'windows' }; break;
    case 'account/read': result = { requiresOpenaiAuth: true, account: { type: 'chatgpt', email: 'secret@example.com' } }; break;
    case 'account/rateLimits/read': result = { rateLimits: { primary: { usedPercent: 12 } } }; break;
    case 'config/read': result = { origins: { instructions: { name: { type: 'user' } } } }; break;
    case 'thread/start': threadParams = r.params; result = { thread: { id: 'thread-1' } }; notify('thread/started', { thread: { id: 'thread-1' } }); break;
    case 'thread/resume': result = { thread: { id: 'thread-1', turns: [{ id: 'turn-1', status: mode === 'stale-resume' ? 'completed' : 'inProgress' }] } }; if (mode === 'resume') setTimeout(finish, 15); break;
    case 'turn/start':
      if (r.params.input[0].text !== 'Scoped task only') throw new Error('Unexpected prompt');
      if (threadParams.sandbox !== 'read-only' || threadParams.threadSource !== 'darkstar') throw new Error('Invalid thread mapping');
      result = { turn: { id: 'turn-1' } };
      setTimeout(() => {
        notify('turn/started', { threadId: 'thread-1', turn: { id: 'turn-1' } });
        if (mode === 'approval') send({ id: 'approval-1', method: 'item/commandExecution/requestApproval', params: { ...scope, itemId: 'cmd-1', command: 'git status' } });
        else if (mode === 'tool') {
          if (r.params.outputSchema !== undefined || threadParams.dynamicTools[0].name !== 'journal_items') throw new Error('Tool output configuration mismatch');
          send({ id: 'tool-1', method: 'item/tool/call', params: { ...scope, callId: 'call-1', tool: 'journal_items', arguments: { operation: 'read' } } });
        } else if (mode === 'wrong-scope') notify('turn/completed', { threadId: 'other', turn: { id: 'turn-1', status: 'completed' } });
        else if (mode !== 'wait') finish();
      }, 15);
      break;
    case 'turn/interrupt': setTimeout(() => notify('turn/completed', { threadId: 'thread-1', turn: { id: 'turn-1', status: 'interrupted' } }), 5); break;
    case 'thread/unsubscribe': break;
    default: throw new Error('Unknown RPC ' + r.method);
  }
  send({ id: r.id, result });
});
