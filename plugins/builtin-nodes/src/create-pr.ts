import { defineNode } from './shared';
import { executionKinds } from './execution-kinds';

export const createPR = defineNode('create-pr', ['delivery.create_pr'], {
 executionKind: executionKinds.create_pr,
 execute: async (args, host) => {
  return host.call('delivery.create_pr', {
   configuration: args.configuration,
   inputs: args.inputs
  });
 }
});
