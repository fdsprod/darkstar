import { defineNode } from './shared';
import { executionKinds } from './execution-kinds';

export const gitCommit = defineNode('git-commit', ['delivery.commit'], {
 executionKind: executionKinds.git_commit,
 execute: async (args, host) => {
  return host.call('delivery.commit', {
   configuration: args.configuration,
   inputs: args.inputs
  });
 }
});
