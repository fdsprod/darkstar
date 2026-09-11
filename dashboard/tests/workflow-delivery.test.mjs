import test from 'node:test';
import assert from 'node:assert/strict';
import {addNode,createStarterDocument,deriveEditorGraph,inspectNode,updateNodeExecutor} from '../src/pages/workflowEditorModel.ts';

test('standalone delivery nodes round trip with typed ports and no added checkpoints',()=>{
  let document = createStarterDocument('delivery');
  for (const [type,id] of [['git_commit','commit'],['git_push','push'],['create_pr','pr']]) {
    document = addNode(document,type,id).document;
    const node = inspectNode(document,id);
    assert.ok(node,`${type} is inspectable`);
    assert.equal(node.executor.type,type);
    assert.equal(node.checkpoint.mode,'none');
    assert.ok(node.outputs.every(output=>output.type.startsWith('schema:')));
  }
  const pr = inspectNode(document,'pr');
  document = updateNodeExecutor(document,'pr',{...pr.executor,base:'release',draft:true});
  assert.equal(inspectNode(document,'pr').executor.base,'release');
  assert.equal(inspectNode(document,'pr').executor.draft,true);
  assert.equal(document.apiVersion,'darkstar.local/v1alpha3');
  assert.equal(deriveEditorGraph(document,{}).nodes.length,4);
  assert.equal(Object.values(document.spec.nodes).some(node=>node.type==='approval'||node.type==='workspace_validate'),false);
});
