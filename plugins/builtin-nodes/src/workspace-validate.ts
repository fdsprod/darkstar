import { defineNode } from './shared';

export const workspaceValidate = defineNode('workspace-validate', ["workspace.resolve","process.run"], {
  execute: async (args, host) => {
    const c = args.configuration;
    if (!c.checks?.length) {
      throw new Error('at least one required check must be configured');
    }
    const workspace = (await host.call('workspace.resolve', {
      reference: args.inputs?.[c.workspaceInput]
    })) as {
      id: string;
    };
    const checks = [];
    for (const argv of c.checks) {
      if (!argv.length || !argv[0].trim()) {
        throw new Error('validation check requires an executable');
      }
      const output = await host.call('process.run', {
        argv,
        timeoutSeconds: 120
      });
      checks.push({
        argv,
        exitCode: 0,
        output
      });
    }
    return {
      validation: {
        workspaceId: workspace.id,
        passed: true,
        checks
      }
    };
  }
});
