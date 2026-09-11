import test from 'node:test';
import assert from 'node:assert/strict';
import { addStagePreset } from '../src/pages/workflowStageModel.ts';

test('stage presets preserve existing resource versions and scope optional links explicitly', () => {
  const oldTemplate = { type: 'template', resource: { kind: 'template', version: '1.0.0', content: '# Old' } };
  const document = { apiVersion: 'darkstar.local/v1alpha3', spec: { inputs: { design_template: oldTemplate }, nodes: { product_design: { type: 'reasoning' } } } };
  const preset = {
    id: 'product_design', name: 'Product Design',
    node: { type: 'reasoning', checkpoint: { mode: 'approve' }, prompt: { id: 'prompt', version: '1.0.0', digest: 'a'.repeat(64) }, inputs: { template: { type: 'template', from: 'run.input.design_template' } } },
    inputs: { design_template: { type: 'template', resource: { kind: 'template_reference', reference: { id: 'template', version: '2.0.0', digest: 'b'.repeat(64) } } } },
    optionalInputs: { open_items: { type: 'open_items' }, deferred_work: { type: 'open_items' } },
  };
  const result = addStagePreset(document, preset);
  assert.equal(result.id, 'product_design_2');
  assert.equal(result.document.spec.nodes[result.id].inputs.template.from, 'run.input.design_template_2');
  assert.deepEqual(result.document.spec.inputs.design_template, oldTemplate);
  assert.deepEqual(result.document.spec.nodes[result.id].checkpoint, { mode: 'approve' });
  assert.equal(result.document.spec.nodes[result.id].inputs.open_items, undefined);
  assert.equal(document.spec.nodes.product_design_2, undefined);
  const again = addStagePreset(result.document, preset);
  assert.equal(again.document.spec.nodes[again.id].inputs.template.from, 'run.input.design_template_2');
  assert.equal(Object.keys(again.document.spec.inputs).length, 2);
});
