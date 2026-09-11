package workflowchat

const workspaceExample = `{
 "apiVersion":"darkstar.local/v1alpha3","kind":"Workflow",
 "metadata":{"name":"workflow/implementation","version":"0.1.0","displayName":"Implement and validate"},
 "spec":{
  "inputs":{"task":{"type":"task","resource":{"kind":"task"}},"repository":{"type":"repository","resource":{"kind":"repository"}}},
  "routeDefaults":{"entry":"prepare","terminals":["validate"]},
  "nodes":{
   "prepare":{"type":"workspace_prepare","displayName":"Prepare workspace","entry":true,"terminal":false,"inputs":{"repository":{"type":"repository","from":"run.input.repository"}},"outputs":{"workspace":{"type":"workspace"}},"workspacePrepare":{"repositoryInput":"repository","checkout":{"mode":"current_checkout"}},"transitions":[{"id":"prepared","to":"implement","kind":"normal"}]},
   "implement":{"type":"implementation","displayName":"Implement work item","entry":false,"terminal":false,"inputs":{"task":{"type":"task","from":"run.input.task"},"workspace":{"type":"workspace","from":"node.prepare.output.workspace"}},"outputs":{"changeset":{"type":"schema:changeset_v1"}},"implementation":{"taskInput":"task","workspaceInput":"workspace","instructions":"Implement the requested task in the connected workspace; preserve unrelated work. Do not publish."},"permissions":["process.run","workspace.write"],"transitions":[{"id":"implemented","to":"validate","kind":"normal"}]},
   "validate":{"type":"workspace_validate","displayName":"Validate workspace","entry":false,"terminal":true,"inputs":{"workspace":{"type":"workspace","from":"node.prepare.output.workspace"}},"outputs":{"validation":{"type":"schema:validation_v1"}},"workspaceValidate":{"workspaceInput":"workspace","checks":[["git","diff","--check"]]},"transitions":[]}
  }
 }
}`
