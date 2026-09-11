import { defineNode } from './shared';
import { executionKinds } from './execution-kinds';

export const deliveryText = defineNode('delivery-text', [], {
  executionKind: executionKinds.delivery_text,
  buildTask: (args) => {
    if (args.permissions?.length) {
      throw new Error('delivery text is a read-only reasoning task');
    }
    return {
      agent: 'delivery-text',
      instructions: 'Prepare commit and pull-request text from the connected Changeset and optional connected task. Read those inputs once. Use the changeset summary, files, and recorded check results; do not reread the repository or full diff. Copy snapshotDigest into changesetSnapshot exactly. Never invent validation results. Return DeliveryText containing commitSubject, commitBody, prTitle, prBody, and changesetSnapshot. Do not commit, push, create a pull request, or perform delivery. ' + (args.configuration.instructions ?? ''),
      skills: [],
      tools: [],
      access: 'read_only'
    };
  },
  configureOutputs: (args) => {
    return structuredClone(args.properties ?? {});
  }
});
