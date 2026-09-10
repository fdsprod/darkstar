package pluginprocess

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"

	"darkstar/src/ports/extension"
)

// builtinBundle is generated with node packages/plugin-sdk/build.mjs. All SDK
// and resource implementation code is bundled so the digest covers dependencies.
//
//go:embed builtin.mjs
var builtinBundle []byte

func BuiltinRef() extension.Ref {
	digest := sha256.Sum256(builtinBundle)
	return extension.Ref{ID: "darkstar/builtin-resources", Version: "1.0.0", Digest: hex.EncodeToString(digest[:])}
}

// MaterializeBuiltin writes the embedded bundle beneath a daemon-owned directory.
// The returned entrypoint is reverified on every process start.
func MaterializeBuiltin(directory string) (string, error) {
	path := filepath.Join(directory, BuiltinRef().Digest, "plugin.mjs")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, builtinBundle, 0600); err != nil {
		return "", err
	}
	return path, nil
}
