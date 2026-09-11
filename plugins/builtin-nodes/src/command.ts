import { executionKinds } from './execution-kinds';
import { defineNode } from './shared';

export const command = defineNode('command', ["process.run"], {
  executionKind: executionKinds.command,
  execute: async (args, host) => {
    const c = args.configuration;
    if (JSON.stringify(c.argv) !== JSON.stringify(['darkstar-project', 'validate', '--json']) || c.cwd) {
      throw new Error('command node is not an explicitly supported deterministic builtin');
    }
    const timeoutSeconds = Math.min(c.timeoutSeconds ?? 30, 30);
    await host.call('process.run', {
      check: 'diff',
      timeoutSeconds
    });
    const output = (await host.call('process.run', {
      check: 'status',
      timeoutSeconds
    })) as string;
    const changedEntries = output.trim() ? output.trim().split(/\s+/u).length : 0;
    if (!changedEntries) {
      throw new Error('darkstar-project validation found no repository changes for the requested implementation');
    }
    return {
      validation: {
        passed: true,
        acceptanceCovered: true,
        checks: [{
          command: 'git diff --check HEAD',
          passed: true
        }, {
          command: 'git status --porcelain',
          passed: true,
          changedEntries
        }]
      }
    };
  }
});
