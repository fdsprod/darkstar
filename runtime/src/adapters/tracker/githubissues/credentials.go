package githubissues

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"

	"darkstar/src/ports"
)

// GHCredential identifies one existing GitHub CLI account. The map key is the
// protected-reference name used by Config; secrets are never command arguments.
type GHCredential struct {
	Host, Login string
}

type GHCredentialResolver struct {
	executable string
	accounts   map[string]GHCredential
}

// NewGHCredentialResolver reuses gh's existing account credential storage and
// requires no token persistence in DARKSTAR. Account selection is explicit;
// changing gh's active account cannot silently change this connection.
func NewGHCredentialResolver(executable string, accounts map[string]GHCredential) (*GHCredentialResolver, error) {
	if executable == "" {
		executable = "gh"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, fail(ports.FailureUnavailable, "GitHub CLI is not executable")
	}
	copyAccounts := make(map[string]GHCredential, len(accounts))
	for ref, account := range accounts {
		if strings.TrimSpace(ref) == "" || !hostPattern.MatchString(account.Host) || !loginPattern(account.Login) {
			return nil, fail(ports.FailureInvalidRequest, "GitHub CLI credential reference requires a valid host and account")
		}
		copyAccounts[ref] = account
	}
	return &GHCredentialResolver{executable: resolved, accounts: copyAccounts}, nil
}

func (r *GHCredentialResolver) Resolve(ctx context.Context, ref string) (string, error) {
	account, ok := r.accounts[ref]
	if !ok {
		return "", fail(ports.FailureUnauthenticated, "GitHub CLI credential reference is not configured")
	}
	command := exec.CommandContext(ctx, r.executable, "auth", "token", "--hostname", account.Host, "--user", account.Login)
	configureCommand(command)
	// Ignore process-level override tokens so --user retains its authority.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, "GH_TOKEN") && !strings.EqualFold(name, "GITHUB_TOKEN") && !strings.EqualFold(name, "GH_ENTERPRISE_TOKEN") && !strings.EqualFold(name, "GITHUB_ENTERPRISE_TOKEN") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	output := &limitedSecretBuffer{}
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.overflow {
		return "", fail(ports.FailureUnauthenticated, "GitHub CLI could not resolve the selected account credential")
	}
	token := strings.TrimSpace(output.String())
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", fail(ports.FailureUnauthenticated, "GitHub CLI returned an invalid account credential")
	}
	return token, nil
}

type limitedSecretBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedSecretBuffer) Write(value []byte) (int, error) {
	if b.Len()+len(value) > 16<<10 {
		b.overflow = true
		return len(value), nil
	}
	return b.Buffer.Write(value)
}

func loginPattern(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}
