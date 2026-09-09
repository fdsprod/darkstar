import test from 'node:test';
import assert from 'node:assert/strict';
import {addNode,createStarterDocument,deriveEditorGraph,inspectNode,updateNodeExecutor} from '../src/pages/workflowEditorModel.ts';
import {derivePortGraph,bindPorts} from '../src/pages/workflowPortModel.ts';

test('workspace components expose required connectors and preserve checkout choices',()=>{
 let {document,nodeId}=addNode(createStarterDocument('workspace'),'workspace_prepare','prepare');
 assert.equal(document.spec.inputs.repository.resource.kind,'repository');
 const prepare=inspectNode(document,nodeId);
 document=updateNodeExecutor(document,nodeId,{...prepare.executor,checkout:{mode:'new_worktree',baseRef:'refs/heads/trunk',branch:'darkstar/{runId}'}});
 assert.equal(inspectNode(document,nodeId).executor.checkout.baseRef,'refs/heads/trunk');
 document=addNode(document,'implementation','implement').document;
 document.spec.nodes.implement.inputs.workspace.from='node.missing.output.workspace';
 let ports=derivePortGraph(document,deriveEditorGraph(document,{}));
 assert.ok(ports.findings.some(f=>f.nodeId==='implement'&&f.field==='inputs.workspace.from'));
 const bound=bindPorts(document,ports.ports.find(p=>p.kind==='data'&&p.nodeId==='prepare'&&p.portId==='workspace'),ports.ports.find(p=>p.kind==='data'&&p.nodeId==='implement'&&p.portId==='workspace'),ports.ports);
 assert.equal(bound.kind,'changed');document=bound.document;
 ports=derivePortGraph(document,deriveEditorGraph(document,{}));
 assert.equal(ports.findings.filter(f=>f.nodeId==='implement').length,0);
 document=addNode(document,'workspace_validate','validate').document;
 const validator=inspectNode(document,'validate');assert.equal(validator.executor.workspaceInput,'workspace');
 document=updateNodeExecutor(document,'validate',{...validator.executor,checks:[['git','diff','--check']]});
 assert.deepEqual(inspectNode(document,'validate').executor.checks,[['git','diff','--check']]);
});
