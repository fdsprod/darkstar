import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFile, realpath, lstat } from 'node:fs/promises';
import { isAbsolute, relative, sep, resolve, basename } from 'node:path';
import { serveProvider, type ProviderHost, type ProviderPlugin } from '../../../packages/plugin-sdk/src/provider';
type Value = Record<string, any>;
const hash = (value: string) => {
  return createHash('sha256').update(value).digest('hex');
};
const versions = ['0.151.0-alpha.7.1', '0.151.0-alpha.7.2', '0.153.4'];
const fingerprint = hash('app_server=v2|artifact_text_input=v1|explicit_skill_input=v2|interactions=json-rpc|local_image_input=v2|resume=thread-id|structured_output=json-schema|text_input=v1|workspace_write=sandbox');
const now = () => {
  return new Date().toISOString();
};
const sleep = (ms: number) => {
  return new Promise<void>(done => {
    return setTimeout(done, ms);
  });
};

/** Owns Codex framing and RPC correlation, including simultaneous host tool calls. */
class AppServer {
  child: ChildProcessWithoutNullStreams;
  version = '';
  identity: Value = {};
  private sequence = 0;
  private pending = new Map<number, {
    resolve(value: any): void;
    reject(error: Error): void;
    timer: ReturnType<typeof setTimeout>;
  }>();
  private queued: Value[] = [];
  private receiver?: (message: Value) => void;
  private buffer = '';
  exited = false;
  closing = false;
  failure?: Error;
  onClose?: (error: Error) => void;
  constructor(config: Value) {
    const env = Array.isArray(config.Environment) ? Object.fromEntries(config.Environment.map((entry: string) => {
      const at = entry.indexOf('=');
      return [entry.slice(0, at), entry.slice(at + 1)];
    })) : config.Environment;
    this.child = spawn(config.Executable, config.Arguments ?? ['app-server'], {
      env: env ?? process.env,
      windowsHide: true,
      shell: false,
      stdio: 'pipe'
    });
    this.child.stderr.resume(); // Provider stderr may contain private text; never promote it into health.
    this.child.stdout.setEncoding('utf8');
    this.child.stdout.on('data', (chunk: string) => {
      this.buffer += chunk;
      if (Buffer.byteLength(this.buffer) > 16 * 1024 * 1024) {
        this.fail(new Error('Codex frame exceeds limit'));
        return;
      }
      let at: number;
      while ((at = this.buffer.indexOf('\n')) >= 0) {
        const line = this.buffer.slice(0, at);
        this.buffer = this.buffer.slice(at + 1);
        if (!line.trim()) {
          continue;
        }
        try {
          const message = JSON.parse(line);
          if (message.method) {
            this.receiver ? this.receiver(message) : this.queued.push(message);
          } else {
            const waiter = this.pending.get(message.id);
            if (!waiter) {
              throw new Error('Codex returned an uncorrelated response');
            }
            this.pending.delete(message.id);
            clearTimeout(waiter.timer);
            message.error ? waiter.reject(new Error(`Codex RPC failed (${message.error.code})`)) : waiter.resolve(message.result);
          }
        } catch {
          this.fail(new Error('Invalid Codex protocol frame'));
        }
      }
    });
    this.child.on('error', () => {
      return this.fail(new Error('Codex process could not start'));
    });
    this.child.on('exit', () => {
      this.exited = true;
      this.fail(new Error('Codex process exited'));
    });
  }
  private fail(error: Error) {
    this.failure ??= error;
    for (const waiter of this.pending.values()) {
      clearTimeout(waiter.timer);
      waiter.reject(error);
    }
    this.pending.clear();
    if (!this.closing) {
      this.onClose?.(error);
    }
  }
  send(message: unknown) {
    if (this.exited) {
      throw new Error('Codex process is closed');
    }
    this.child.stdin.write(JSON.stringify(message) + '\n');
  }
  call(method: string, params: unknown, timeout = 30000): Promise<any> {
    if (this.failure) {
      return Promise.reject(this.failure);
    }
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`Codex ${method} timed out`));
      }, timeout);
      this.pending.set(id, {
        resolve,
        reject,
        timer
      });
      this.send({
        id,
        method,
        params
      });
    });
  }
  listen(receiver: (message: Value) => void) {
    this.receiver = receiver;
    this.queued.splice(0).forEach(receiver);
  }
  async initialize() {
    this.identity = await this.call('initialize', {
      clientInfo: {
        name: 'darkstar',
        title: 'DARKSTAR',
        version: '0.1.0'
      },
      capabilities: {
        experimentalApi: true
      }
    });
    this.version = String(this.identity.userAgent ?? '').match(/(?:Codex|Desktop)\/(\d+\.\d+\.\d+(?:-[^\s)]+)?)/)?.[1] ?? '';
    if (!this.identity.codexHome || !this.identity.platformFamily || !versions.includes(this.version)) {
      throw new Error('Unsupported Codex App Server identity or version');
    }
    this.send({
      method: 'initialized',
      params: {}
    });
  }
  async close(threadID?: string): Promise<boolean> {
    this.closing = true;
    if (threadID && !this.exited) {
      try {
        await this.call('thread/unsubscribe', {
          threadId: threadID
        }, 1500);
      } catch {
      // ownership cleanup continues
    }
    }
    this.child.stdin.end();
    for (let i = 0; i < 30 && !this.exited; i++) {
      await sleep(10);
    }
    if (!this.exited) {
      this.child.kill();
    }
    for (let i = 0; i < 50 && !this.exited; i++) {
      await sleep(10);
    }
    return this.exited;
  }
}
function kindOf(message: Value): string {
  const p = message.params ?? {};
  const method = message.method;
  const fixed: Record<string, string> = {
    'thread/started': 'attempt.started',
    'turn/started': 'turn.started',
    'item/agentMessage/delta': 'message.delta',
    'item/commandExecution/outputDelta': 'command.output',
    'turn/plan/updated': 'plan.updated',
    'item/plan/delta': 'plan.updated',
    'thread/tokenUsage/updated': 'usage.updated',
    error: 'error',
    'thread/realtime/error': 'error',
    warning: 'warning',
    configWarning: 'warning',
    guardianWarning: 'warning',
    'windows/worldWritableWarning': 'warning',
    deprecationNotice: 'warning'
  };
  if (fixed[method]) {
    return fixed[method];
  }
  if (method === 'turn/completed') {
    return p.turn?.status === 'interrupted' ? 'turn.interrupted' : 'turn.completed';
  }
  if (method === 'thread/status/changed' && p.status?.activeFlags) {
    return 'attempt.waiting';
  }
  if (method === 'item/started' || method === 'item/completed') {
    const started = method === 'item/started';
    const type = p.item?.type;
    if (type === 'agentMessage') {
      return started ? 'unknown.provider_event' : 'message.completed';
    }
    if (type === 'plan') {
      return 'plan.updated';
    }
    if (type === 'usageLimitExceeded') {
      return 'error';
    }
    if (type === 'commandExecution') {
      return started ? 'command.started' : 'command.completed';
    }
    if (type === 'fileChange') {
      return started ? 'file_change.started' : 'file_change.completed';
    }
    if (['mcpToolCall', 'dynamicToolCall', 'webSearch', 'imageGeneration', 'collabAgentToolCall', 'findInPage', 'listFiles', 'read', 'search', 'openPage'].includes(type)) {
      return started ? 'tool.started' : 'tool.completed';
    }
  }
  return 'unknown.provider_event';
}
function checkpoint(message: Value): Value | undefined {
  const p = message.params ?? {};
  const method = message.method;
  let kind: string;
  let target: string;
  let operation: string;
  let subject: string = p.itemId ?? '';
  switch (method) {
    case 'execCommandApproval':
    case 'item/commandExecution/requestApproval':
      if (p.networkApprovalContext) {
        kind = 'network';
        target = 'network_host';
        operation = 'connect';
        subject = p.networkApprovalContext.host ?? subject;
      } else {
        kind = 'command';
        target = 'command';
        operation = 'execute';
        subject = Array.isArray(p.command) ? p.command.join(' ') : p.command ?? '';
      }
      break;
    case 'applyPatchApproval':
    case 'item/fileChange/requestApproval':
      kind = 'file';
      target = 'file';
      operation = 'modify';
      subject = p.path ?? p.filePath ?? '';
      break;
    case 'item/permissions/requestApproval':
      kind = 'permission';
      target = 'provider_capabilities';
      operation = 'elevate';
      subject = Object.keys(p.permissions ?? {}).sort().join(',');
      break;
    case 'item/tool/requestUserInput':
    case 'mcpServer/elicitation/request':
      kind = 'user';
      target = 'user_input';
      operation = 'answer';
      subject ||= 'request';
      break;
    case 'item/tool/call':
      kind = 'tool';
      target = 'provider_tool';
      operation = 'invoke';
      subject = p.tool ?? subject;
      break;
    default:
      return undefined;
  }
  if (!p.turnId || message.id === undefined) {
    throw new Error('Interaction is missing its identity');
  }
  const result: Value = {
    kind,
    providerRequestId: String(message.id),
    providerTurnId: p.turnId,
    scope: {
      target,
      operation,
      ...(subject ? {
        subject
      } : {})
    },
    scopeDigest: hash(JSON.stringify({
      kind,
      providerMethod: method,
      params: p
    })),
    policyDigest: hash(`provider-interaction-policy-v1\0${kind}\0ask`)
  };
  if (kind === 'user') {
    const questions = Array.isArray(p.questions) ? p.questions : p.questions ? [p.questions] : [];
    if (!questions.length || questions.some((q: Value) => {
      return q.isSecret;
    })) {
      throw new Error('User input must contain non-secret questions');
    }
    result.input = {
      questions: questions.map((q: Value, i: number) => {
        const options = (q.options ?? []).map((o: Value) => {
          return o.label;
        });
        return {
          id: q.id || `question_${i + 1}`,
          prompt: q.question || q.prompt || q.header,
          options,
          schema: {
            type: 'string',
            ...(options.length ? {
              allowedValues: options
            } : {})
          }
        };
      })
    };
  }
  return result;
}
interface Attempt {
  request: Value;
  digest: string;
  operation: string;
  client: AppServer;
  host: ProviderHost;
  handle: Value;
  events: Value[];
  sequence: number;
  interactions: Map<string, {
    message: Value;
    checkpoint: Value;
  }>;
  responses: Map<string, {
    digest: string;
    receipt: Value;
  }>;
  chain: Promise<void>;
  result?: Value;
  usage: Value;
  evidence: Value[];
  latestOutput?: string;
  stopping?: string;
  start?: Promise<Value>;
  timeout?: ReturnType<typeof setTimeout>;
  completion?: Promise<void>;
  stopObserved?: boolean;
}
class CodexProvider implements ProviderPlugin {
  id = 'darkstar/provider-codex';
  version = '1.0.0';
  name = 'codex';
  private config?: Value;
  private attempts = new Map<string, Attempt>();
  async close() {
    await Promise.all([...this.attempts.values()].filter(s => {
      return s.client;
    }).map(s => {
      return s.client.close(s.handle.ProviderThreadID);
    }));
  }
  async handle(method: string, request: Value, host: ProviderHost): Promise<unknown> {
    switch (method) {
      case 'provider.shutdown':
        await this.close();
        return {};
      case 'provider.configure':
        if (this.config && JSON.stringify(this.config) !== JSON.stringify(request)) {
          throw new Error('Provider configuration is immutable');
        }
        if (!isAbsolute(request.Executable ?? '')) {
          throw new Error('Provider executable must be an absolute host-resolved path');
        }
        this.config = request;
        return {};
      case 'provider.health':
        return this.health();
      case 'provider.capabilities':
        return this.capabilities();
      case 'provider.start':
        return this.start(request, host, false);
      case 'provider.resume':
        return this.start(request, host, true);
      case 'provider.events':
        {
          const state = this.state(request.Handle);
          if (!Number.isSafeInteger(request.AfterSequence) || request.AfterSequence < (state.request.LastSequence ?? 0) || request.AfterSequence > state.sequence) {
            throw new Error('Invalid event cursor');
          }
          for (let n = 0; n < 100 && !state.result && state.sequence <= request.AfterSequence; n++) {
            await sleep(20);
          }
          const available = state.events.filter(e => {
            return e.Sequence > request.AfterSequence;
          });
          const events = [];
          let bytes = 0;
          for (const event of available) {
            const size = Buffer.byteLength(JSON.stringify(event));
            if (events.length && (events.length >= 128 || bytes + size > 1024 * 1024)) {
              break;
            }
            events.push(event);
            bytes += size;
          }
          return {
            Events: events,
            Terminal: !!state.result && events.length === available.length
          };
        }
      case 'provider.result':
        {
          const state = this.state(request.Handle);
          while (!state.result) {
            await sleep(20);
          }
          return state.result;
        }
      case 'provider.respond':
        return this.respond(request);
      case 'provider.cancel':
        return this.cancel(request);
      default:
        throw new Error('Unknown provider operation');
    }
  }
  private state(handle: Value): Attempt {
    const state = this.attempts.get(handle?.AttemptID);
    if (!state || !['AttemptID', 'Provider', 'ProviderThreadID', 'ProviderTurnID', 'ProcessOwnerID'].every(k => {
      return state.handle[k] === handle[k];
    })) {
      throw new Error('Unknown or mismatched attempt handle');
    }
    return state;
  }
  private capabilities() {
    const features: Record<string, Value> = {};
    for (const [id, version] of Object.entries({
      app_server: 'v2',
      artifact_text_input: 'v1',
      text_input: 'v1',
      structured_output: 'json-schema',
      workspace_write: 'sandbox',
      interactions: 'json-rpc',
      resume: 'thread-id',
      local_image_input: 'v2',
      explicit_skill_input: 'v2'
    })) {
      features[id] = {
        Kind: 'available',
        Version: version
      };
    }
    features.local_image_input.Metadata = {
      inputType: 'localImage',
      mediaTypes: 'image/jpeg,image/png,image/webp',
      details: 'auto,low,high,original'
    };
    features.explicit_skill_input.Metadata = {
      inputType: 'skill',
      locator: 'bounded-local-SKILL.md'
    };
    return {
      Provider: 'codex',
      Fingerprint: fingerprint,
      Features: features,
      ObservedAt: now()
    };
  }
  private async client() {
    if (!this.config) {
      throw new Error('Provider is not configured');
    }
    const client = new AppServer(this.config);
    try {
      await client.initialize();
      return client;
    } catch (error) {
      await client.close();
      throw error;
    }
  }
  private async health() {
    const result: Value = {
      State: 'unavailable',
      Provider: 'codex',
      ProviderVersion: '',
      ExecutableIdentity: this.config?.Executable ?? '',
      Platform: '',
      Authentication: 'unknown',
      Usage: 'unknown',
      InstructionSources: [],
      Diagnostics: []
    };
    let client: AppServer | undefined;
    try {
      client = await this.client();
      Object.assign(result, {
        State: 'available',
        ProviderVersion: client.version,
        Platform: client.identity.platformFamily
      });
      const account = await client.call('account/read', {
        refreshToken: false
      });
      if (typeof account.requiresOpenaiAuth !== 'boolean') {
        throw new Error('Incomplete health');
      }
      if (account.requiresOpenaiAuth && !account.account) {
        result.State = 'unauthenticated';
        result.Authentication = 'unauthenticated';
      } else {
        result.Authentication = 'authenticated';
        const limits = (await client.call('account/rateLimits/read', {})).rateLimits;
        if (!limits || [limits.primary, limits.secondary].some(w => {
          return w && typeof w.usedPercent !== 'number';
        })) {
          throw new Error('Incomplete usage');
        }
        const exhausted = limits.rateLimitReachedType || limits.spendControlReached || [limits.primary, limits.secondary].some(w => {
          return w?.usedPercent >= 100;
        });
        result.Usage = exhausted ? 'exhausted' : 'ready';
        if (exhausted) {
          result.State = 'usage_exhausted';
        }
      }
      const config = await client.call('config/read', {
        cwd: this.config?.ProjectRoot,
        includeLayers: false
      });
      if (!config.origins) {
        throw new Error('Incomplete instruction origins');
      }
      result.InstructionSources = Object.entries(config.origins).filter(([k]) => {
        return ['instructions', 'developer_instructions'].includes(k);
      }).map(([k, v]: [string, any]) => {
        return `${v.name?.type || 'unknown'}:${k}`;
      }).sort();
    } catch {
      result.State = client ? 'degraded' : 'unavailable';
      result.Diagnostics.push('Codex readiness could not be verified');
    } finally {
      if (client && !(await client.close())) {
        result.State = 'degraded';
        result.Diagnostics.push('Codex health ownership release failed');
      }
    }
    return result;
  }
  private async start(request: Value, host: ProviderHost, resume: boolean) {
    if (!request.AttemptID || !request.IdempotencyKey) {
      throw new Error('Attempt and idempotency key are required');
    }
    const digest = hash(JSON.stringify(request));
    const operation = resume ? 'resume' : 'start';
    const existing = this.attempts.get(request.AttemptID);
    if (existing) {
      if (existing.digest !== digest || existing.operation !== operation) {
        throw new Error('Attempt identity conflicts with earlier request');
      }
      return existing.start;
    }
    // Reserve before asynchronous process creation, so duplicate calls cannot create two turns.
    const reservation = {
      digest,
      operation
    } as Attempt;
    this.attempts.set(request.AttemptID, reservation);
    reservation.start = this.begin(request, host, resume, reservation);
    return reservation.start;
  }
  private async begin(request: Value, host: ProviderHost, resume: boolean, state: Attempt) {
    if (resume && (!request.ProviderThreadID || !request.ProviderTurnID || !request.ContextDigest || !request.WorkspaceDigest)) {
      throw new Error('Resume requires recorded identities and context digests');
    }
    if (!resume) {
      await validateRequest(request);
    }
    const client = await this.client();
    Object.assign(state, {
      request,
      client,
      host,
      handle: {},
      events: [],
      sequence: request.LastSequence ?? 0,
      interactions: new Map(),
      responses: new Map(),
      chain: Promise.resolve(),
      usage: {},
      evidence: []
    });
    try {
      let threadID: string;
      let turnID: string;
      if (resume) {
        const thread = (await client.call('thread/resume', {
          threadId: request.ProviderThreadID
        })).thread;
        if (thread?.id !== request.ProviderThreadID || !thread.turns?.some((t: Value) => {
          return t.id === request.ProviderTurnID && ['inProgress', 'in_progress'].includes(t.status);
        })) {
          throw new Error('Recorded turn is not active in resumed thread');
        }
        threadID = thread.id;
        turnID = request.ProviderTurnID;
      } else {
        const ask = [request.CommandPolicy, request.FilePolicy, request.ToolPolicy].includes('ask');
        const thread = (await client.call('thread/start', {
          dynamicTools: request.DynamicTools ?? [],
          cwd: request.Workspace,
          ephemeral: false,
          sandbox: request.Access === 'workspace_write' ? 'workspace-write' : 'read-only',
          approvalPolicy: ask ? 'on-request' : 'never',
          ...(ask ? {
            approvalsReviewer: 'user'
          } : {}),
          threadSource: 'darkstar',
          model: request.ModelHint || undefined,
          runtimeWorkspaceRoots: [request.Workspace, ...(request.AdditionalRoots ?? [])].sort()
        })).thread;
        if (!thread?.id) {
          throw new Error('Thread start omitted identity');
        }
        threadID = thread.id;
        const input = [{
          type: 'text',
          text: request.Prompt
        }, ...(request.Inputs ?? []).map((i: Value) => {
          return i.Kind === 'image' ? {
            type: 'localImage',
            path: i.Locator,
            detail: i.Detail
          } : i.Kind === 'skill' ? {
            type: 'skill',
            name: i.Name,
            path: i.Locator
          } : {
            type: 'text',
            text: i.Text
          };
        })];
        const turn = (await client.call('turn/start', {
          threadId: threadID,
          input,
          ...(request.ToolBackedOutputs ? {} : {
            outputSchema: request.OutputSchema
          }),
          model: request.ModelHint || undefined,
          effort: request.ReasoningHint || undefined
        })).turn;
        if (!turn?.id) {
          throw new Error('Turn start omitted identity');
        }
        turnID = turn.id;
      }
      state.handle = {
        AttemptID: request.AttemptID,
        Provider: 'codex',
        ProviderThreadID: threadID,
        ProviderTurnID: turnID,
        ProcessOwnerID: String(client.child.pid)
      };
      client.onClose = () => {
        if (!state.result && !state.stopping) {
          void this.complete(state, 'unknown', undefined, 'Codex closed before a terminal turn');
        }
      };
      client.listen(message => {
        state.chain = state.chain.then(() => {
          return this.observe(state, message);
        }).catch(() => {
          return this.complete(state, 'unknown', undefined, 'Codex event processing failed');
        });
      });
      if (request.Timeout > 0) {
        state.timeout = setTimeout(() => {
          void this.stop(state, 'timeout', request.CancellationGrace);
        }, request.Timeout / 1e6);
      }
      return state.handle;
    } catch (error) {
      await client.close();
      throw error;
    }
  }
  private metadata(state: Attempt) {
    return {
      Usage: state.usage,
      WorkspaceEvidence: state.evidence,
      Recovery: {
        ProviderThreadID: state.handle.ProviderThreadID,
        ProviderTurnID: state.handle.ProviderTurnID,
        ProcessOwnerID: state.handle.ProcessOwnerID,
        LastSequence: state.sequence,
        Resumable: !state.result,
        EvidenceRef: state.evidence.at(-1)?.Ref ?? ''
      }
    };
  }
  private async emit(state: Attempt, message: Value, kind: string, payload: Value) {
    const p = message.params ?? {};
    const sequence = state.sequence + 1;
    const evidence = await state.host.call('evidence.record', {
      AttemptID: state.request.AttemptID,
      Sequence: sequence,
      Kind: kind,
      MediaType: 'application/json',
      Data: message
    });
    if (!evidence?.Ref) {
      throw new Error('Host did not persist provider evidence');
    }
    state.sequence = sequence;
    state.evidence.push(evidence);
    state.events.push({
      SchemaVersion: 1,
      AttemptID: state.request.AttemptID,
      Sequence: sequence,
      OccurredAt: message.emittedAtMs ? new Date(message.emittedAtMs).toISOString() : now(),
      Kind: kind,
      Provider: 'codex',
      ProviderVersion: state.client.version,
      ProviderThreadID: p.threadId || p.conversationId || p.thread?.id || state.handle.ProviderThreadID,
      ProviderTurnID: p.turnId || p.turn?.id || state.handle.ProviderTurnID,
      ProviderItemID: p.itemId || p.callId || p.item?.id || (message.id !== undefined ? String(message.id) : ''),
      Payload: payload,
      RawEvidenceRef: evidence.Ref
    });
  }
  private async observe(state: Attempt, original: Value) {
    if (state.result) {
      return;
    }
    let message = original;
    let p = message.params ?? {};
    if (p.threadId && p.threadId !== state.handle.ProviderThreadID || p.thread?.id && p.thread.id !== state.handle.ProviderThreadID || p.turnId && p.turnId !== state.handle.ProviderTurnID || p.turn?.id && p.turn.id !== state.handle.ProviderTurnID) {
      throw new Error('Provider event belongs to another attempt');
    }
    if (message.method === 'item/tool/call' && (state.request.DynamicTools ?? []).some((t: Value) => {
      return t.name === p.tool;
    })) {
      if (p.threadId !== state.handle.ProviderThreadID || p.turnId !== state.handle.ProviderTurnID) {
        throw new Error('Tool call omitted its attempt scope');
      }
      let output: Value;
      try {
        const value = await state.host.call('tool.call', {
          AttemptID: state.request.AttemptID,
          CallID: p.callId,
          Name: p.tool,
          Arguments: p.arguments
        });
        output = {
          success: true,
          contentItems: [{
            type: 'inputText',
            text: JSON.stringify(value)
          }]
        };
      } catch {
        output = {
          success: false,
          contentItems: [{
            type: 'inputText',
            text: 'Host tool invocation failed'
          }]
        };
      }
      state.client.send({
        id: message.id,
        result: output
      });
      message = {
        method: 'item/completed',
        params: {
          threadId: p.threadId,
          turnId: p.turnId,
          item: {
            type: 'dynamicToolCall',
            id: p.callId,
            tool: p.tool,
            arguments: p.arguments,
            output
          }
        }
      };
      p = message.params;
    }
    let kind = kindOf(message);
    const payload: Value = {
      providerMethod: message.method,
      ...(message.id !== undefined ? {
        requestId: message.id
      } : {}),
      params: p
    };
    if (message.id !== undefined) {
      const value = checkpoint(message);
      if (value) {
        state.interactions.set(String(message.id), {
          message,
          checkpoint: value
        });
        payload.checkpoint = value;
        kind = value.kind === 'user' ? 'user_input.requested' : value.kind === 'tool' ? 'tool.started' : 'permission.requested';
      } else {
        state.client.send({
          id: message.id,
          error: {
            code: -32601,
            message: 'Unsupported provider interaction'
          }
        });
      }
    }
    if (message.method === 'serverRequest/resolved') {
      const tracked = state.interactions.get(String(p.requestId));
      if (tracked) {
        payload.checkpoint = tracked.checkpoint;
        kind = tracked.checkpoint.kind === 'user' ? 'user_input.response_recorded' : tracked.checkpoint.kind === 'tool' ? 'tool.completed' : 'permission.response_recorded';
        state.interactions.delete(String(p.requestId));
      }
    }
    if (message.method === 'item/completed' && ['commandExecution', 'fileChange'].includes(p.item?.type)) {
      await state.host.call('markdown.capture', {
        AttemptID: state.request.AttemptID,
        Key: p.item.id
      });
    }
    await this.emit(state, original, kind, payload);
    if (kind === 'usage.updated' && p.tokenUsage?.total) {
      const t = p.tokenUsage.total;
      state.usage = {
        InputTokens: t.inputTokens ?? 0,
        CachedTokens: t.cachedInputTokens ?? 0,
        OutputTokens: t.outputTokens ?? 0
      };
    }
    if (kind === 'message.completed' && (!p.item.phase || p.item.phase === 'final_answer')) {
      state.latestOutput = p.item.text;
    }
    if (kind === 'turn.interrupted') {
      state.stopObserved = true;
      if (!state.stopping) {
        await this.complete(state, 'interrupted', undefined, 'Codex turn was interrupted');
      } else {
        await this.complete(state, state.stopping === 'timeout' ? 'failed' : 'cancelled', undefined, state.stopping === 'timeout' ? 'Provider attempt timed out' : undefined);
      }
    }
    if (kind === 'turn.completed') {
      if (p.turn?.status !== 'completed') {
        await this.complete(state, 'failed', undefined, 'Codex turn failed');
        return;
      }
      let output: unknown;
      try {
        output = state.request.ToolBackedOutputs ? await state.host.call('outputs.resolve', {
          AttemptID: state.request.AttemptID
        }) : JSON.parse(state.latestOutput ?? '');
      } catch {
        await this.complete(state, 'failed', undefined, 'Structured outputs could not be assembled');
        return;
      }
      await this.emit(state, {
        method: 'darkstar/derived',
        params: {
          threadId: state.handle.ProviderThreadID,
          turnId: state.handle.ProviderTurnID,
          itemId: 'structured-output'
        },
        output
      }, 'structured_output.completed', output as Value);
      await this.complete(state, 'succeeded', output);
    }
  }
  private complete(state: Attempt, kind: string, output?: unknown, message?: string): Promise<void> {
    if (state.completion) {
      return state.completion;
    }
    state.completion = (async () => {
      clearTimeout(state.timeout);
      const closed = await state.client.close(state.handle.ProviderThreadID);
      if (!closed) {
        kind = 'unknown';
        message = 'Provider ownership release was not confirmed';
      }
      if (state.stopping) {
        const payload = {
          reason: state.stopping,
          disposition: closed ? state.stopObserved ? 'graceful' : 'forced' : 'uncertain'
        };
        try {
          await this.emit(state, {
            method: 'darkstar/derived',
            params: {
              threadId: state.handle.ProviderThreadID,
              turnId: state.handle.ProviderTurnID
            },
            outcome: payload
          }, state.stopping === 'timeout' ? 'attempt.failed' : 'attempt.cancelled', payload);
        } catch {
          kind = 'unknown';
          message = 'Provider stop evidence could not be persisted';
        }
      }
      state.result = {
        Kind: kind,
        ...this.metadata(state),
        ...(kind === 'succeeded' ? {
          StructuredOutput: output
        } : {}),
        ...(message ? {
          Failure: {
            Code: kind === 'unknown' ? 'uncertain' : kind === 'interrupted' ? 'interrupted' : state.stopping === 'timeout' ? 'timeout' : 'invalid_request',
            Message: message,
            Retryable: kind === 'interrupted'
          }
        } : {})
      };
      state.result.Recovery.Resumable = false;
    })();
    return state.completion;
  }
  private async respond(request: Value) {
    const state = this.attempts.get(request.AttemptID);
    if (!state || request.ProviderThreadID !== state.handle.ProviderThreadID || !request.IdempotencyKey) {
      throw new Error('Interaction response identity mismatch');
    }
    const digest = hash(JSON.stringify(request));
    const prior = state.responses.get(request.IdempotencyKey);
    if (prior) {
      if (prior.digest !== digest) {
        throw new Error('Conflicting response retry');
      }
      return prior.receipt;
    }
    const tracked = state.interactions.get(request.ProviderRequestID);
    if (!tracked || tracked.checkpoint.scopeDigest !== request.ScopeDigest) {
      throw new Error('Interaction scope mismatch');
    }
    const {
      message,
      checkpoint: cp
    } = tracked;
    let result: Value;
    if (cp.kind === 'user' || cp.kind === 'tool') {
      if (request.Decision || request.Answer === undefined) {
        throw new Error('Interaction requires an answer');
      }
      result = request.Answer;
    } else {
      if (!['allow_once', 'allow_for_session', 'deny', 'cancel', 'expire'].includes(request.Decision) || request.Answer !== undefined) {
        throw new Error('Interaction requires a permission decision');
      }
      if (cp.kind === 'permission') {
        const permissions: Value = {};
        if (request.Decision.startsWith('allow_')) {
          for (const key of ['network', 'fileSystem']) {
            if (message.params.permissions?.[key]) {
              permissions[key] = message.params.permissions[key];
            }
          }
        }
        result = {
          permissions,
          scope: request.Decision === 'allow_for_session' ? 'session' : 'turn'
        };
      } else {
        if (['execCommandApproval', 'applyPatchApproval'].includes(message.method)) {
          result = {
            decision: ({
              allow_once: 'approved',
              allow_for_session: 'approved_for_session',
              deny: {
                denied: {
                  rejection: 'rejected by user'
                }
              },
              expire: 'timed_out',
              cancel: 'abort'
            } as Value)[request.Decision]
          };
        } else {
          result = {
            decision: ({
              allow_once: 'accept',
              allow_for_session: 'acceptForSession',
              deny: 'decline',
              cancel: 'cancel',
              expire: 'cancel'
            } as Value)[request.Decision]
          };
        }
      }
    }
    state.client.send({
      id: message.id,
      result
    });
    const receipt = {
      ProviderRequestID: request.ProviderRequestID,
      RecordedAt: now()
    };
    state.responses.set(request.IdempotencyKey, {
      digest,
      receipt
    });
    return receipt;
  }
  private async stop(state: Attempt, cause: string, grace: number) {
    if (state.result) {
      return {
        Disposition: 'already_terminal',
        EvidenceRef: state.evidence.at(-1)?.Ref ?? ''
      };
    }
    state.stopping = cause;
    try {
      await state.client.call('turn/interrupt', {
        threadId: state.handle.ProviderThreadID,
        turnId: state.handle.ProviderTurnID
      }, Math.max(100, Math.min(grace / 1e6 || 1000, 5000)));
    } catch {
      // force and confirm below
    }
    const deadline = Date.now() + Math.max(100, Math.min(grace / 1e6 || 1000, 5000));
    while (!state.result && Date.now() < deadline) {
      await sleep(20);
    }
    const graceful = !!state.result;
    if (!state.result) {
      await this.complete(state, cause === 'timeout' ? 'failed' : 'cancelled', undefined, cause === 'timeout' ? 'Provider attempt timed out' : undefined);
    }
    return {
      Disposition: (state.result as Value | undefined)?.Kind === 'unknown' ? 'uncertain' : graceful ? 'graceful' : 'forced',
      EvidenceRef: state.evidence.at(-1)?.Ref ?? ''
    };
  }
  private cancel(request: Value) {
    if (!request.IdempotencyKey) {
      throw new Error('Cancellation requires an idempotency key');
    }
    return this.stop(this.state(request.Handle), 'cancel', request.GracePeriod);
  }
}
async function validateRequest(request: Value) {
  if (!request.RunID || !request.NodeID || !request.Prompt?.trim() || !isAbsolute(request.Workspace ?? '') || !['read_only', 'workspace_write'].includes(request.Access) || !request.OutputSchema || request.Timeout < 0 || request.CancellationGrace < 0) {
    throw new Error('Invalid scoped attempt request');
  }
  if (request.CapabilityFingerprint && request.CapabilityFingerprint !== fingerprint) {
    throw new Error('Capability fingerprint changed');
  }
  if (request.Network !== 'denied' || ![request.CommandPolicy, request.FilePolicy, request.ToolPolicy].every(p => {
    return ['deny', 'ask', 'allow'].includes(p);
  })) {
    throw new Error('Unsupported execution policy');
  }
  const roots = await Promise.all([request.Workspace, ...(request.AdditionalRoots ?? [])].map((p: string) => {
    return realpath(p);
  }));
  for (const root of roots) {
    if (!(await lstat(root)).isDirectory()) {
      throw new Error('Workspace roots must be directories');
    }
  }
  request.Workspace = roots[0];
  request.AdditionalRoots = roots.slice(1);
  for (const input of request.Inputs ?? []) {
    if (!input.Name?.trim() || !input.MediaType?.trim() || !input.Locator?.trim() || !/^[a-f0-9]{64}$/.test(input.Digest ?? '')) {
      throw new Error('Prepared input requires name, media type, locator and digest');
    }
    if (!['text', 'artifact', 'image', 'skill'].includes(input.Kind)) {
      throw new Error('Unsupported input kind');
    }
    if (['text', 'artifact'].includes(input.Kind)) {
      if (!input.Text?.trim() || input.Detail || hash(input.Text) !== input.Digest) {
        throw new Error('Input digest mismatch or invalid text input');
      }
      continue;
    }
    if (input.Text) {
      throw new Error('File input cannot also contain prepared text');
    }
    if (input.Kind === 'image' && !input.Detail) {
      input.Detail = 'auto';
    }
    if (input.Kind === 'image' && (!['image/png', 'image/jpeg', 'image/webp'].includes(input.MediaType) || !['auto', 'low', 'high', 'original'].includes(input.Detail))) {
      throw new Error('Unsupported image input');
    }
    if (input.Kind === 'skill' && (input.MediaType !== 'text/markdown' || input.Detail || basename(input.Locator).toLowerCase() !== 'skill.md')) {
      throw new Error('Invalid explicit skill input');
    }
    if (!isAbsolute(input.Locator ?? '') || !/^[a-f0-9]{64}$/.test(input.Digest ?? '')) {
      throw new Error('File input requires an absolute path and digest');
    }
    const path = resolve(input.Locator);
    const root = roots.find(root => {
      const rel = relative(root, path);
      return rel !== '..' && !rel.startsWith('..' + sep) && !isAbsolute(rel);
    });
    if (!root) {
      throw new Error('Input file is outside granted roots');
    }
    let current = root;
    for (const part of relative(root, path).split(sep).filter(Boolean)) {
      current = resolve(current, part);
      if ((await lstat(current)).isSymbolicLink()) {
        throw new Error('Input file contains a symlink');
      }
    }
    const actual = createHash('sha256').update(await readFile(path)).digest('hex');
    if (actual !== input.Digest) {
      throw new Error('Input file digest mismatch');
    }
    input.Locator = path;
  }
}
serveProvider(new CodexProvider());
