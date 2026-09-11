import { executionKinds } from './execution-kinds';
import { defineNode } from './shared';

export const reasoning = defineNode('reasoning', [], {
  executionKind: executionKinds.reasoning,
  buildTask: (args) => {
    const c = args.configuration;
    if (args.permissions?.length) {
      throw new Error('workflow node names permission policies that are not configured: ' + args.permissions.join(', '));
    }
    return {
      agent: c.agent,
      instructions: 'Complete the following task using only the supplied inputs. ' + (c.instructions ?? ''),
      skills: c.skills ?? [],
      tools: c.tools ?? [],
      access: 'read_only'
    };
  },
  configureOutputs: (args) => {
    return structuredClone(args.properties ?? {});
  }
});
