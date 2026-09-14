package sqlite

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"darkstar/src/ports/artifactregistry"
)

func TestInvestigationArtifactSubtypeRemainsImmutableAcrossRebuild(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "provenance.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	request := artifactRequest(testID("artifact", 'J'), "investigation-origin", strings.Repeat("a", 64))
	request.SourceKind = artifactregistry.SourceGenerated
	request.Provenance = artifactregistry.InvestigationProvenance{CollectionID: "investigation_one", UnitID: "investigation_unit_one", AttemptID: testID("attempt", 'J'), OperationID: testID("operation", 'J')}
	value, _, err := database.Register(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE investigation_artifact_provenance SET unit_id='another'`,
		`DELETE FROM investigation_artifact_provenance`,
		`INSERT INTO investigation_artifact_provenance VALUES('missing',1,'collection','unit','attempt')`,
	} {
		if _, err := database.SQL().ExecContext(t.Context(), statement); err == nil {
			t.Fatal("immutable or dangling provenance mutation succeeded")
		}
	}
	if err := database.RebuildProjections(t.Context()); err != nil {
		t.Fatal(err)
	}
	latest, err := database.LatestVersion(t.Context(), value.ArtifactID)
	if err != nil || !reflect.DeepEqual(latest.Provenance, value.Provenance) {
		t.Fatalf("canonical provenance changed after rebuild: %#v, %v", latest.Provenance, err)
	}
	listed, err := database.Artifacts(t.Context())
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Provenance, value.Provenance) {
		t.Fatalf("artifact listing lost investigation subtype: %#v, %v", listed, err)
	}
}
