package typescript

import (
	"crypto/sha256"
	"darkstar/src/ports/extension"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed builtin.mjs
var builtin []byte

func BuiltinRef() extension.Ref {
	return extension.Ref{ID: "darkstar/provider-codex", Version: "1.0.0", Digest: fmt.Sprintf("%x", sha256.Sum256(builtin))}
}
func MaterializeBuiltin(directory string) (string, error) {
	name := filepath.Join(directory, BuiltinRef().Digest, "provider.mjs")
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(name, builtin, 0600); err != nil {
		return "", err
	}
	return name, nil
}
