import { executionKinds } from './execution-kinds';
import { defineNode } from './shared';

export const workspacePrepare = defineNode('workspace-prepare', ["workspace.prepare"], {
  executionKind: executionKinds.workspace_prepare,
  execute: async (args, host) => {
    const c = args.configuration;
    if (!['current_checkout', 'new_worktree'].includes(c.checkout?.mode)) {
      throw new Error('choose a supported checkout mode');
    }
    if (!args.inputs || !Object.hasOwn(args.inputs, c.repositoryInput)) {
      throw new Error('connect the repository input to Prepare workspace');
    }
    const workspace = await host.call('workspace.prepare', {
      repository: args.inputs[c.repositoryInput],
      checkout: c.checkout
    });
    return {
      workspace
    };
  }
});
