import { type HostServices, type Node } from '../../../packages/plugin-sdk/src/index';

export type Arguments = {
  operation: 'buildTask' | 'configureOutputs' | 'execute';
  configuration: Record<string, any>;
  permissions?: string[];
  properties?: Record<string, unknown>;
  inputs?: Record<string, unknown>;
};
export const changeset = () => {
  return {
    type: 'object',
    additionalProperties: false,
    properties: {
      summary: {
        type: 'string'
      },
      files: {
        type: 'array',
        items: {
          type: 'string'
        }
      },
      validation: {
        type: 'array',
        items: {
          type: 'string'
        }
      }
    },
    required: ['summary', 'files', 'validation']
  };
};
export function writable(permissions: string[] = []) {
  if (JSON.stringify([...permissions].sort()) !== JSON.stringify(['process.run', 'workspace.write'])) {
    throw new Error('workspace node requires exactly process.run and workspace.write permissions');
  }
}

export type NodeBehavior = {
  executionKind: 'llm';
  buildTask: (args: Arguments) => unknown;
  configureOutputs: (args: Arguments) => unknown;
  execute?: never;
} | {
  executionKind: 'deterministic';
  buildTask?: never;
  configureOutputs?: never;
  execute: (args: Arguments, host: HostServices) => Promise<unknown>;
};

export function defineNode(id: string, requiredCapabilities: string[], behavior: NodeBehavior): Node {
  return {
    id,
    executionKind: behavior.executionKind,
    description: `Built-in ${id} node behavior`,
    requiredCapabilities,
    inputSchema: {
      type: 'object',
      additionalProperties: false,
      properties: {
        operation: {
          enum: ['buildTask', 'configureOutputs', 'execute']
        },
        configuration: {
          type: 'object'
        },
        permissions: {
          type: 'array',
          items: {
            type: 'string'
          }
        },
        properties: {
          type: 'object'
        },
        inputs: {
          type: 'object'
        }
      },
      required: ['operation', 'configuration']
    },
    resultSchema: {
      type: 'object'
    },
    invoke: async (arguments_, host) => {
      const args = arguments_ as Arguments;
      if (args.operation === 'buildTask') {
        if (!behavior.buildTask) {
          throw new Error('node does not build agent tasks');
        }
        return behavior.buildTask(args);
      }
      if (args.operation === 'configureOutputs') {
        if (!behavior.configureOutputs) {
          throw new Error('node does not configure agent outputs');
        }
        return behavior.configureOutputs(args);
      }
      if (args.operation !== 'execute') {
        throw new Error('unknown node operation');
      }
      if (!behavior.execute) {
        throw new Error('node does not execute deterministic work');
      }
      return behavior.execute(args, host);
    }
  };
}
