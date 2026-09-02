package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	providerport "darkstar/src/ports/provider"
)

type healthProbeScript struct {
	authenticated bool
	exhausted     bool
	done          chan error
}

func newHealthProbeScript(authenticated, exhausted bool) *healthProbeScript {
	return &healthProbeScript{authenticated: authenticated, exhausted: exhausted, done: make(chan error, 1)}
}

func (script *healthProbeScript) factory(ctx context.Context) (*AppServerClient, InitializeResult, error) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	client, err := NewAppServerClient(clientWrites, clientReads, AppServerOptions{
		ClientInfo: ClientInfo{Name: "darkstar-health-test", Version: "1.0.0"},
	})
	if err != nil {
		return nil, InitializeResult{}, err
	}
	go func() { script.done <- script.run(serverReads, serverWrites) }()
	initialized, err := client.Initialize(ctx)
	if err != nil {
		return nil, InitializeResult{}, err
	}
	return client, initialized, nil
}

func (script *healthProbeScript) run(reader *io.PipeReader, writer *io.PipeWriter) error {
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	scanner := bufio.NewScanner(reader)
	receive := func(method string) (wireMessage, error) {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return wireMessage{}, err
			}
			return wireMessage{}, io.EOF
		}
		var message wireMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return wireMessage{}, err
		}
		if message.Method != method {
			return wireMessage{}, errors.New("health probe received unexpected method " + message.Method)
		}
		return message, nil
	}
	send := func(id json.RawMessage, result any) error {
		payload, err := json.Marshal(map[string]any{"id": id, "result": result})
		if err != nil {
			return err
		}
		_, err = writer.Write(append(payload, '\n'))
		return err
	}

	initialize, err := receive("initialize")
	if err != nil {
		return err
	}
	if err := send(initialize.ID, map[string]any{
		"userAgent": "Codex Desktop/0.151.0-alpha.7.2 (Windows 11; x86_64)",
		"codexHome": `C:\Users\test\.codex`, "platformFamily": "windows", "platformOs": "windows",
	}); err != nil {
		return err
	}
	if _, err := receive("initialized"); err != nil {
		return err
	}
	account, err := receive("account/read")
	if err != nil {
		return err
	}
	accountResult := map[string]any{"requiresOpenaiAuth": true, "account": nil}
	if script.authenticated {
		accountResult["account"] = map[string]any{"type": "chatgpt", "email": "secret@example.com", "planType": "plus"}
	}
	if err := send(account.ID, accountResult); err != nil {
		return err
	}
	if script.authenticated {
		limits, err := receive("account/rateLimits/read")
		if err != nil {
			return err
		}
		used := 42
		reached := any(nil)
		if script.exhausted {
			used = 100
			reached = "rate_limit_reached"
		}
		if err := send(limits.ID, map[string]any{"rateLimits": map[string]any{
			"primary": map[string]any{"usedPercent": used}, "secondary": nil,
			"rateLimitReachedType": reached, "spendControlReached": false,
			"credits": map[string]any{"balance": "secret-balance"},
		}}); err != nil {
			return err
		}
	}
	config, err := receive("config/read")
	if err != nil {
		return err
	}
	if err := send(config.ID, map[string]any{
		"config": map[string]any{},
		"origins": map[string]any{
			"instructions":           map[string]any{"name": map[string]any{"type": "user"}, "version": "1"},
			"developer_instructions": map[string]any{"name": map[string]any{"type": "project"}, "version": "1"},
			"api_key":                map[string]any{"name": map[string]any{"type": "user"}, "version": "1"},
		},
	}); err != nil {
		return err
	}
	if scanner.Scan() {
		return errors.New("health probe sent an unexpected request after config/read")
	}
	return scanner.Err()
}

func TestAdapterProbeHealthReportsSafeAuthUsageVersionAndInstructionSources(t *testing.T) {
	t.Parallel()
	script := newHealthProbeScript(true, false)
	adapter := newHealthTestAdapter(t, script)
	observation, err := adapter.ProbeHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-script.done; err != nil {
		t.Fatal(err)
	}
	if observation.State != providerport.HealthAvailable || observation.ProviderVersion != "0.151.0-alpha.7.2" ||
		observation.Authentication != providerport.AuthenticationAuthenticated || observation.Usage != providerport.UsageReady {
		t.Fatalf("health = %#v", observation)
	}
	wantSources := []string{"project:developer_instructions", "user:instructions"}
	if !reflect.DeepEqual(observation.InstructionSources, wantSources) {
		t.Fatalf("instruction sources = %#v, want %#v", observation.InstructionSources, wantSources)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret@example.com", "secret-balance"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("health leaked %q: %s", secret, encoded)
		}
	}
}

func TestAdapterProbeHealthDistinguishesAuthenticationAndUsageExhaustion(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		authenticated bool
		exhausted     bool
		state         providerport.HealthState
	}{
		"authentication": {false, false, providerport.HealthUnauthenticated},
		"usage":          {true, true, providerport.HealthUsageExhausted},
	} {
		t.Run(name, func(t *testing.T) {
			script := newHealthProbeScript(test.authenticated, test.exhausted)
			adapter := newHealthTestAdapter(t, script)
			observation, err := adapter.ProbeHealth(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := <-script.done; err != nil {
				t.Fatal(err)
			}
			if observation.State != test.state {
				t.Fatalf("health state = %q, want %q", observation.State, test.state)
			}
		})
	}
}

func TestAdaptersReportFinalInputCapabilities(t *testing.T) {
	t.Parallel()
	manifest := appServerCapabilityManifest(time.Unix(1, 0))
	names := make([]string, 0, len(manifest.Features))
	for name := range manifest.Features {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{
		"app_server", "artifact_text_input", "explicit_skill_input", "interactions", "local_image_input",
		"resume", "structured_output", "text_input", "workspace_write",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("App Server capabilities = %#v, want %#v", names, want)
	}

	execAdapter := newExecTestAdapter(t, nil, func(ExecCommand) (ExecProcess, error) {
		return nil, errors.New("capability inspection must not start a process")
	})
	execManifest, err := execAdapter.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"artifact_text_input", "exec_json", "resume", "structured_output", "text_input"} {
		if _, available := execManifest.Features[name].(providerport.AvailableCapability); !available {
			t.Errorf("exec capability %s is not available", name)
		}
	}
	for _, name := range []string{"explicit_skill_input", "interactions", "local_image_input", "workspace_write"} {
		if _, unavailable := execManifest.Features[name].(providerport.UnavailableCapability); !unavailable {
			t.Errorf("exec capability %s is not unavailable", name)
		}
	}
}

func newHealthTestAdapter(t *testing.T, script *healthProbeScript) *Adapter {
	t.Helper()
	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewAdapter(AdapterOptions{
		Factory: script.factory, EvidenceRecorder: recorder, ProjectRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}
