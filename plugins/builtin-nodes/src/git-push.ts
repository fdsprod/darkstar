import { defineNode } from './shared';
import { executionKinds } from './execution-kinds';

export const gitPush = defineNode('git-push', ['delivery.push'], {
 executionKind: executionKinds.git_push,
 execute: async (args, host) => {
  return host.call('delivery.push', {
   configuration: args.configuration,
   inputs: args.inputs
  });
 }
});
