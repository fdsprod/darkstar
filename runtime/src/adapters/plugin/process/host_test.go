package pluginprocess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/extension"
	"darkstar/src/ports/plugin"
)

func node(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for cross-language plugin tests")
	}
	return path
}
func builtinHost(t *testing.T) *Host {
	t.Helper()
	path, err := MaterializeBuiltin(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Executable: node(t), Entrypoint: path, Ref: BuiltinRef(), GrantedCapabilities: []string{"journal.read", "journal.mutate", "workspace.read", "workspace.write", "workspace.publish_artifact"}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func TestBuiltinTypeScriptTools(t *testing.T) {
	h := builtinHost(t)
	descriptor, err := h.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Ref != BuiltinRef() || len(descriptor.Resources) != 2 || len(descriptor.Tools) != 3 {
		t.Fatalf("unexpected descriptor %+v", descriptor)
	}
	for _, resource := range descriptor.Resources {
		if resource.Category != "data" {
			t.Fatalf("journal must declare its DATA category: %+v", resource)
		}
	}
	cases := []struct{ id, args, method string }{
		{"darkstar/open-items", `{"operation":"read"}`, "journal.read"},
		{"darkstar/open-items", `{"operation":"add","entryId":"","text":"Investigate","key":"k","expectedRevision":0}`, "journal.mutate"},
		{"darkstar/decision-log", `{"operation":"record","entryId":"","text":"Use TS","key":"k","expectedRevision":0}`, "journal.mutate"},
		{"workspace.write", `{"area":"staged","path":"report.md","content":{"encoding":"utf8","text":"hello"}}`, "workspace.write"},
	}
	for _, tc := range cases {
		t.Run(tc.id+tc.method, func(t *testing.T) {
			called := false
			result, err := h.Invoke(context.Background(), plugin.Invocation{Contribution: tc.id, Arguments: json.RawMessage(tc.args)}, plugin.HostServicesFunc(func(ctx context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
				called = true
				if method != tc.method {
					t.Fatalf("method %s", method)
				}
				switch method {
				case "journal.read":
					return json.RawMessage(`{"markdown":"journal","revision":0}`), nil
				case "journal.mutate":
					return json.RawMessage(`{"status":"recorded","entryId":"item_1","revision":1}`), nil
				default:
					return json.RawMessage(`{"status":"stored","size":5,"digest":"` + strings.Repeat("a", 64) + `"}`), nil
				}
			}))
			if err != nil || !called || !json.Valid(result) {
				t.Fatalf("result %s, called %v, error %v", result, called, err)
			}
		})
	}
}
func TestCancelledInvocationCannotCallHost(t *testing.T) {
	h := builtinHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.Invoke(ctx, plugin.Invocation{Contribution: "darkstar/open-items", Arguments: json.RawMessage(`{"operation":"read"}`)}, plugin.HostServicesFunc(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("expired invocation called host")
		return nil, nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
func TestBuiltinRejectsInvalidArgumentsBeforeHostMutation(t *testing.T) {
	h := builtinHost(t)
	for _, args := range []string{`{"operation":"approve"}`, `{"operation":"add","text":"x","key":"k","entryId":"forged"}`, `{"operation":"read","resource":"another-owner"}`} {
		_, err := h.Invoke(context.Background(), plugin.Invocation{Contribution: "darkstar/open-items", Arguments: json.RawMessage(args)}, plugin.HostServicesFunc(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			t.Fatal("unexpected callback")
			return nil, nil
		}))
		if err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}
func fixture(t *testing.T, action string) *Host {
	t.Helper()
	source := `import {createInterface} from 'node:readline';
const d={protocol:'darkstar.plugin/v1',ref:{id:'test/fixture',version:'1.0.0'},resources:[],tools:[{id:'fixture',description:'test',inputSchema:{type:'object'},resultSchema:{type:'object'},requiredCapabilities:['allowed']}]};
createInterface({input:process.stdin}).on('line',line=>{const m=JSON.parse(line); if(m.method==='describe') process.stdout.write(JSON.stringify({type:'response',id:m.id,result:d})+'\n'); else { ` + action + ` } });`
	path := filepath.Join(t.TempDir(), "fixture.mjs")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(source))
	h, err := New(Config{Executable: node(t), Entrypoint: path, Ref: extension.Ref{ID: "test/fixture", Version: "1.0.0", Digest: hex.EncodeToString(digest[:])}, GrantedCapabilities: []string{"allowed"}, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func TestDeniedCallback(t *testing.T) {
	h := fixture(t, `process.stdout.write(JSON.stringify({type:'host_call',id:'h',method:'denied',params:{}})+'\n');`)
	_, err := h.Invoke(context.Background(), plugin.Invocation{Contribution: "fixture", Arguments: json.RawMessage(`{}`)}, plugin.HostServicesFunc(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("denied capability reached host")
		return nil, nil
	}))
	if err == nil || !strings.Contains(err.Error(), "CAPABILITY_DENIED") {
		t.Fatalf("%v", err)
	}
}
func TestInvalidResponseAndResult(t *testing.T) {
	for _, action := range []string{`process.stdout.write('not-json\n');`, `process.stdout.write(JSON.stringify({type:'response',id:'wrong',result:{}})+'\n');`, `process.stdout.write(JSON.stringify({type:'response',id:'1',result:42})+'\n');`, `process.stdout.write('x'.repeat(1048578)+'\n');`} {
		h := fixture(t, action)
		_, err := h.Invoke(context.Background(), plugin.Invocation{Contribution: "fixture", Arguments: json.RawMessage(`{}`)}, nil)
		if err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}
func TestTimeoutAndDigestReplacement(t *testing.T) {
	h := fixture(t, `setInterval(()=>{},1000);`)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.Invoke(ctx, plugin.Invocation{Contribution: "fixture", Arguments: json.RawMessage(`{}`)}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout result %v after %v", err, time.Since(start))
	}
	if err := os.WriteFile(h.config.Entrypoint, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Describe(context.Background()); err == nil || !strings.Contains(err.Error(), "DIGEST_MISMATCH") {
		t.Fatalf("replacement: %v", err)
	}
}
