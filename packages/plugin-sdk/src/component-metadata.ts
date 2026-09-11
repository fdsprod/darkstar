/** Display roles are separate from the execution interface used by the daemon. */
export type ComponentCategory = 'llm' | 'deterministic' | 'data' | 'artifact' | 'template' | 'control' | 'hitl';
export type ResourceCategory = Extract<ComponentCategory, 'data' | 'artifact' | 'template'>;

export const componentBadgeLabels = {
  llm: 'LLM',
  deterministic: 'DETERMINISTIC',
  data: 'DATA',
  artifact: 'ARTIFACT',
  template: 'TEMPLATE',
  control: 'CONTROL',
  hitl: 'HITL'
} as const satisfies Record<ComponentCategory, string>;

export const builtinResourceCategories = {
  task: 'data',
  repository: 'data',
  workspace: 'data',
  artifact: 'artifact',
  markdown: 'artifact',
  template: 'template',
  constant: 'data',
  config: 'data',
  open_items: 'data',
  decision_log: 'data'
} as const satisfies Record<string, ResourceCategory>;

/** Nominal values remain DATA unless their contract identifies a document or template. */
export function resourceCategory(kind: string, valueType?: string): ResourceCategory {
  if (kind === 'artifact' || valueType === 'markdown') {
    return 'artifact';
  }
  if (kind === 'template' || valueType === 'template') {
    return 'template';
  }
  return builtinResourceCategories[kind as keyof typeof builtinResourceCategories] ?? 'data';
}
