import { serve } from '../../../packages/plugin-sdk/src/index';
import { reasoning } from './reasoning';
import { implementation } from './implementation';
import { pointExecution } from './point-execution';
import { workspacePrepare } from './workspace-prepare';
import { workspaceValidate } from './workspace-validate';
import { command } from './command';

serve({
  id: 'darkstar/builtin-nodes',
  version: '1.0.0',
  resources: [],
  nodes: [reasoning, implementation, pointExecution, workspacePrepare, workspaceValidate, command]
});
