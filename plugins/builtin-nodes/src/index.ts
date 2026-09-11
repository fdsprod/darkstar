import { gitCommit } from './git-commit';
import { gitPush } from './git-push';
import { createPR } from './create-pr';
import { serve } from '../../../packages/plugin-sdk/src/index';
import { reasoning } from './reasoning';
import { implementation } from './implementation';
import { pointExecution } from './point-execution';
import { workspacePrepare } from './workspace-prepare';
import { workspaceValidate } from './workspace-validate';
import { deliveryText } from './delivery-text';
import { command } from './command';

serve({
  id: 'darkstar/builtin-nodes',
  version: '1.0.0',
  resources: [],
  nodes: [gitCommit, gitPush, createPR, deliveryText, reasoning, implementation, pointExecution, workspacePrepare, workspaceValidate, command]
});
