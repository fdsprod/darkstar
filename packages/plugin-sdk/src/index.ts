import { createInterface } from 'node:readline';
export const protocol = 'darkstar.plugin/v1';
export type Schema = Record<string, unknown>;
export interface HostServices {
  call(method: string, arguments_: unknown): Promise<unknown>;
}
export interface Tool {
  id: string;
  description: string;
  inputSchema: Schema;
  resultSchema: Schema;
  requiredCapabilities: string[];
  invoke(arguments_: unknown, host: HostServices): Promise<unknown>;
}
export interface Resource {
  kind: string;
  createOperation: string;
  updateOperations: string[];
  tool: Tool;
}
/** Node invocations are daemon-selected behavior, never agent-visible tools. */
export type ExecutionKind = 'deterministic' | 'llm';
export interface Node extends Tool {
  executionKind: ExecutionKind;
}
export interface Plugin {
  id: string;
  version: string;
  resources: Resource[];
  tools?: Tool[];
  nodes?: Node[];
}

/** Stdout belongs to the protocol. Use stderr for diagnostic logging. */
export function serve(plugin: Plugin): void {
  for (const node of plugin.nodes ?? []) {
    if (!['deterministic', 'llm'].includes(node.executionKind)) {
      throw new Error(`Node ${node.id} must declare executionKind`);
    }
  }
  const pending = new Map<string, {
    resolve(value: unknown): void;
    reject(error: Error): void;
  }>();
  let sequence = 0;
  const send = (message: unknown) => {
    return process.stdout.write(JSON.stringify(message) + '\n');
  };
  const host: HostServices = {
    call(method, arguments_) {
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
          params: arguments_
        });
      });
    }
  };
  const lines = createInterface({
    input: process.stdin,
    crlfDelay: Infinity
  });
  lines.on('line', async (line: string) => {
    let request: any;
    try {
      request = JSON.parse(line);
      if (request.type === 'host_result') {
        const waiter = pending.get(request.id);
        if (!waiter) {
          throw new Error('Unknown host result');
        }
        pending.delete(request.id);
        request.error ? waiter.reject(new Error(request.error)) : waiter.resolve(request.result);
        return;
      }
      if (request.type !== 'request') {
        throw new Error('Invalid request type');
      }
      let result: unknown;
      if (request.method === 'describe') {
        result = {
          protocol,
          ref: {
            id: plugin.id,
            version: plugin.version
          },
          resources: plugin.resources,
          tools: plugin.tools ?? [],
          ...(plugin.nodes ? {
            nodes: plugin.nodes
          } : {})
        };
      } else {
        if (request.method === 'invoke') {
          const tool = [...plugin.resources.map(resource => {
            return resource.tool;
          }), ...(plugin.tools ?? []), ...(plugin.nodes ?? [])].find(tool => {
            return tool.id === request.params.contribution;
          });
          if (!tool) {
            throw new Error('Unknown contribution');
          }
          result = await tool.invoke(request.params.arguments, host);
        } else {
          throw new Error('Unknown method');
        }
      }
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
}
