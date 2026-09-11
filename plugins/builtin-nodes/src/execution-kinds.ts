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
