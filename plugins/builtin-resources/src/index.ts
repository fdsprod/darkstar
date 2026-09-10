import { serve, type Resource, type Tool } from '../../../packages/plugin-sdk/src/index';

function journal(kind: string, id: string, createOperation: string, updateOperations: string[]): Resource {
  const operations = ['read', createOperation, ...updateOperations];
  return {
    kind, createOperation, updateOperations,
    tool: {
      id,
      description: `Use this append-only ${kind} journal. Use entryId for an existing item; use an empty entryId to create one. key is a stable operation key, reused on retry. text records the item, decision, or resolution rationale.`,
      inputSchema: { type: 'object', additionalProperties: false, properties: {
        operation: { type: 'string', enum: operations }, entryId: { type: 'string' }, text: { type: 'string' }, key: { type: 'string' }, expectedRevision: { type: 'integer', minimum: 0 },
      }, required: ['operation'] },
      resultSchema: { oneOf: [
        { type: 'object', additionalProperties: false, properties: { markdown: { type: 'string' }, revision: { type: 'integer', minimum: 0 } }, required: ['markdown', 'revision'] },
        { type: 'object', additionalProperties: false, properties: { entryId: { type: 'string' }, status: { enum: ['recorded', 'already_recorded'] }, revision: { type: 'integer', minimum: 1 } }, required: ['entryId', 'status', 'revision'] },
      ] },
      requiredCapabilities: ['journal.read', 'journal.mutate'],
      async invoke(raw, host) {
        const args = raw as { operation: string; entryId: string; text: string; key: string; expectedRevision?: number };
        if (!operations.includes(args.operation)) throw new Error('Unsupported journal operation');
        if (args.operation === 'read') return host.call('journal.read', {});
        if (!Number.isSafeInteger(args.expectedRevision) || args.expectedRevision! < 0) throw new Error('Mutation requires expectedRevision from the latest read');
        if (!args.text?.trim() || !args.key?.trim()) throw new Error('text and key are required');
        if (args.operation === createOperation && args.entryId) throw new Error('New entries cannot supply entryId');
        if (args.operation !== createOperation && !args.entryId) throw new Error('An existing entryId is required');
        return host.call('journal.mutate', args);
      },
    },
  };
}
const content = { oneOf: [
  { type: 'object', additionalProperties: false, properties: { encoding: { const: 'utf8' }, text: { type: 'string' } }, required: ['encoding', 'text'] },
  { type: 'object', additionalProperties: false, properties: { encoding: { const: 'base64' }, data: { type: 'string' } }, required: ['encoding', 'data'] },
] };
function workspaceTool(id: string, description: string, properties: Record<string, unknown>, resultProperties: Record<string, unknown>, required = Object.keys(properties)): Tool {
  return { id, description, inputSchema: { type: 'object', additionalProperties: false, properties, required }, resultSchema: { type: 'object', additionalProperties: false, properties: resultProperties, required: Object.keys(resultProperties) }, requiredCapabilities: [id], invoke: (args, host) => host.call(id, args) };
}
const area = { type: 'string', enum: ['plugin_state', 'scratch', 'staged'] };
const path = { type: 'string', minLength: 1 };
const digest = { type: 'string', pattern: '^[a-f0-9]{64}$' };
serve({ id: 'darkstar/builtin-resources', version: '1.0.0', tools: [
  workspaceTool('workspace.read', 'Read a file and its current digest in this plugin’s work-item workspace.', { area, path }, { content, digest }),
  workspaceTool('workspace.write', 'Create a file, or replace it by supplying expectedDigest from the latest read. Published artifact revisions remain immutable.', { area, path, content, expectedDigest: digest }, { status: { const: 'stored' }, size: { type: 'integer', minimum: 0 }, digest }, ['area', 'path', 'content']),
  workspaceTool('workspace.publish_artifact', 'Publish a staged file as an immutable artifact. This does not submit a workflow output or grant approval.', { path, mediaType: { type: 'string', minLength: 1 }, key: { type: 'string', minLength: 1 } }, { artifact: { type: 'object', additionalProperties: false, properties: { artifactId: { type: 'string', minLength: 1 }, version: { type: 'integer', minimum: 1 } }, required: ['artifactId', 'version'] }, digest, mediaType: { type: 'string' } }),
], resources: [
  journal('open_items', 'darkstar/open-items', 'add', ['resolve', 'defer']),
  journal('decision_log', 'darkstar/decision-log', 'record', ['supersede']),
] });
