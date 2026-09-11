import type { ComponentCategory } from '../../../packages/plugin-sdk/src/component-metadata';

export const executionKinds = {
 git_commit: 'deterministic',
 git_push: 'deterministic',
 create_pr: 'deterministic',
  delivery_text: 'llm',
  reasoning: 'llm',
  implementation: 'llm',
  point_execution: 'llm',
  workspace_prepare: 'deterministic',
  workspace_validate: 'deterministic',
  command: 'deterministic',
  gate: 'deterministic',
  approval: 'deterministic',
  routing: 'deterministic',
  subworkflow: 'deterministic',
  extension: 'deterministic',
  start: 'deterministic',
  done: 'deterministic'
} as const;

/** Every built-in canvas component declares a display role. */
export const componentCategories = {
  git_commit: executionKinds.git_commit,
  git_push: executionKinds.git_push,
  create_pr: executionKinds.create_pr,
  delivery_text: executionKinds.delivery_text,
  reasoning: executionKinds.reasoning,
  implementation: executionKinds.implementation,
  point_execution: executionKinds.point_execution,
  workspace_prepare: executionKinds.workspace_prepare,
  workspace_validate: executionKinds.workspace_validate,
  command: executionKinds.command,
  gate: 'control',
  approval: 'hitl',
  routing: 'control',
  subworkflow: 'control',
  extension: executionKinds.extension,
  start: 'control',
  done: 'control'
} as const satisfies Record<keyof typeof executionKinds, ComponentCategory>;
