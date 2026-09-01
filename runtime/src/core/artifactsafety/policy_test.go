package artifactsafety

import (
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

func TestDefaultPolicyMatchesNormativeBudgetsAndFailsClosed(t *testing.T) {
	t.Parallel()
	policy := DefaultPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	limits := policy.ProcessorLimits()
	if policy.SourceBytes != 25<<20 || limits.OutputBytes != 2<<20 || limits.TableCells != 100_000 || limits.Pages != 200 || limits.Pixels != 40_000_000 || limits.Representations != 8 {
		t.Fatalf("default limits = %#v / %#v", policy, limits)
	}
	if policy.AllowsProcessing(artifactregistry.SensitivityUnknown) || policy.AllowsProcessing(artifactregistry.SensitivitySecret) || !policy.AllowsProcessing(artifactregistry.SensitivityInternal) {
		t.Fatal("classified-only disclosure policy did not fail closed")
	}
}
