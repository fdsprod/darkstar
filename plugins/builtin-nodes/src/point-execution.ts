import { defineNode, changeset, writable } from './shared';

const pointInstructions = 'Read the connected Markdown implementation plan and carry out its points. Implement the requested work item in the supplied workspace. Make only the necessary repository changes. Do not claim completion unless the requested outcome exists on disk. Return changeset with summary, files, and validation; return progress with completed_points and remaining_points.';

export const pointExecution = defineNode('point-execution', [], {
  buildTask: (args) => {
    writable(args.permissions);
    return {
      agent: 'implementation-point',
      instructions: pointInstructions,
      skills: [],
      tools: [],
      access: 'workspace_write'
    };
  },
  configureOutputs: (args) => {
    const properties = structuredClone(args.properties ?? {});
    if (Object.hasOwn(properties, 'changeset')) {
      const schema: any = changeset();
      properties.changeset = schema;
    }
    if (Object.hasOwn(properties, 'progress')) {
      properties.progress = {
        type: 'object',
        additionalProperties: false,
        properties: {
          completed_points: {
            type: 'integer'
          },
          remaining_points: {
            type: 'integer'
          }
        },
        required: ['completed_points', 'remaining_points']
      };
    }
    return properties;
  }
});
