package nodes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/nodeextension"
)

type extensionTestExecutor struct{ received nodeextension.Request }

func (e *extensionTestExecutor) Execute(_ context.Context, r nodeextension.Request) (map[string]json.RawMessage, error) {
	e.received = r
	return map[string]json.RawMessage{"result": json.RawMessage(`"done"`)}, nil
}

type extensionTestResolver struct{ executor *extensionTestExecutor }

func (r extensionTestResolver) Configure(extension.Ref, json.RawMessage) (nodeextension.Executor, error) {
	return r.executor, nil
}

func TestExtensionReceivesOnlyDeclaredInputCopies(t *testing.T) {
	ref := extension.Ref{ID: "test/node", Version: "1.0.0", Digest: strings.Repeat("a", 64)}
	n := Extension{Node: workflow.ExtensionNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"task": workflow.RequiredBinding{}}}, Executor: workflow.ExtensionExecutor{Ref: ref, Configuration: json.RawMessage(`{}`)}}}
	executor := &extensionTestExecutor{}
	inputs := Inputs{"task": json.RawMessage(`"do work"`), "unrelated": json.RawMessage(`"private"`)}
	output, err := n.Execute(context.Background(), inputs, BuiltinServices{Extensions: extensionTestResolver{executor}, RunInputs: map[string]json.RawMessage{"secret": json.RawMessage(`true`)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.received.Inputs) != 1 || string(executor.received.Inputs["task"]) != `"do work"` {
		t.Fatalf("unexpected inputs: %v", executor.received.Inputs)
	}
	executor.received.Inputs["task"][1] = 'X'
	if string(inputs["task"]) != `"do work"` {
		t.Fatal("extension mutated daemon input")
	}
	if string(output) != `{"result":"done"}` {
		t.Fatalf("output=%s", output)
	}
	if _, err := n.Execute(context.Background(), inputs, BuiltinServices{}); err == nil {
		t.Fatal("unregistered extension ran")
	}
}
