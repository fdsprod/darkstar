import { createInterface } from 'node:readline';
/** Frozen read ceiling shared by provider start and resume. Empty roots deny all
 * filesystem reads; absent/null preserves legacy access semantics. A provider
 * may advertise scoped_read_filesystem v1 only with actual read confinement. */
export interface ScopedReadRequirement {
  ReadRoots: string[];
  ScopeDigest: string;
  ConfigurationDigest: string;
  EvidenceDigest: string;
}
export interface ProviderFilesystemRequest {
  Filesystem?: ScopedReadRequirement | null;
  CapabilityFingerprint?: string;
}

/** Use before spawning a provider or dispatching tools in unsupported adapters. */
export function rejectUnsupportedScopedReads(request: ProviderFilesystemRequest): void {
  if (request.Filesystem != null) {
    throw new Error('scoped filesystem reads are unsupported: this provider cannot enforce frozen snapshot read roots. Select a provider with scoped_read_filesystem v1 support.');
  }
}

export interface ProviderHost {
  call(method: string, params: unknown): Promise<any>;
}
/** A provider is a persistent protocol translator. The daemon owns authorization and run state. */
export interface ProviderPlugin {
  id: string;
  version: string;
  name: string;
  handle(method: string, request: any, host: ProviderHost): Promise<unknown>;
  close(): Promise<void>;
}
export function serveProvider(plugin: ProviderPlugin): void {
  let sequence = 0;
  const pending = new Map<string, {
    resolve(value: unknown): void;
    reject(error: Error): void;
  }>();
  const send = (value: unknown) => {
    return process.stdout.write(JSON.stringify(value) + '\n');
  };
  const host: ProviderHost = {
    call(method, params) {
      const id = `host-${++sequence}`;
      return new Promise((resolve, reject) => {
        pending.set(id, {
          resolve,
          reject
        });
        send({
          type: 'host_call',
          id,
          method,
          params
        });
      });
    }
  };
  const lines = createInterface({
    input: process.stdin,
    crlfDelay: Infinity
  });
  lines.on('line', async line => {
    let request: any;
    try {
      if (Buffer.byteLength(line) > 16 * 1024 * 1024) {
        throw new Error('Provider frame exceeds limit');
      }
      request = JSON.parse(line);
      if (request.type === 'host_result') {
        const waiter = pending.get(request.id);
        if (!waiter) {
          throw new Error('Unknown host response');
        }
        pending.delete(request.id);
        request.error ? waiter.reject(new Error(request.error)) : waiter.resolve(request.result);
        return;
      }
      if (request.type !== 'request') {
        throw new Error('Invalid provider request');
      }
      const result = request.method === 'describe' ? {
        protocol: 'darkstar.plugin/v1',
        ref: {
          id: plugin.id,
          version: plugin.version
        },
        providers: [{
          id: plugin.name
        }]
      } : await plugin.handle(request.method, request.params, host);
      send({
        type: 'response',
        id: request.id,
        result
      });
    } catch (error) {
      send({
        type: 'response',
        id: request?.id ?? '',
        error: error instanceof Error ? error.message : String(error)
      });
    }
  });
  const close = async () => {
    for (const waiter of pending.values()) {
      waiter.reject(new Error('Provider host disconnected'));
    }
    pending.clear();
    await plugin.close();
  };
  lines.on('close', () => {
    void close();
  });
  process.on('SIGTERM', () => {
    void close().finally(() => {
      return process.exit(0);
    });
  });
}
