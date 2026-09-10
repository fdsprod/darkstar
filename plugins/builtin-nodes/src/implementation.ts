import { defineNode, changeset, writable } from './shared';

const implementationInstructions = (c: Record<string, any>) => {
  return 'Implement the task in the connected input ' + c.taskInput + " in the supplied workspace. Read optional connected Markdown instructions and supporting inputs when present; a plan is not required. Modify the actual files and run relevant checks. Preserve unrelated work. Do not commit, push, publish, or deploy. Use inspect_workspace_changes to see file changes relative to this attempt's durable baseline. Return changeset with disposition (changed, unchanged, or blocked), summary, files (exact relative paths reported by inspect_workspace_changes), and validation (checks actually run and results). Use unchanged only if the request is already satisfied and no files changed. Use blocked to report a blocker; blocked is not a successful completion. The runtime verifies files against the workspace; merely returning proposed content does not implement the task. " + (c.instructions ?? '');
};

export const implementation = defineNode('implementation', [], {
  buildTask: (args) => {
    writable(args.permissions);
    return {
      agent: 'implementation',
      instructions: implementationInstructions(args.configuration),
      skills: [],
      tools: [],
      access: 'workspace_write'
    };
  },
  configureOutputs: (args) => {
    const properties = structuredClone(args.properties ?? {});
    if (Object.hasOwn(properties, 'changeset')) {
      const schema: any = changeset();
      schema.properties.disposition = {
        type: 'string',
        enum: ['changed', 'unchanged', 'blocked']
      };
      schema.required.unshift('disposition');
      properties.changeset = schema;
    }
    return properties;
  }
});
