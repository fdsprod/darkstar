package codex

import (
	"darkstar/src/ports"
	"darkstar/src/ports/provider"
)

func rejectScopedReads(requirement *provider.ScopedReadRequirement) error {
	if err := provider.ValidateFilesystemRequirement(requirement, provider.CapabilityManifest{}); err != nil {
		return adapterFailure(ports.FailureUnsupported, err.Error(), false)
	}
	return nil
}
