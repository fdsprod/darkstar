package dashboardassets

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedDashboardContainsEntryDocument(t *testing.T) {
	t.Parallel()
	content, err := fs.ReadFile(Files(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(content)), "</head>") {
		t.Fatal("embedded dashboard index is missing the bootstrap insertion point")
	}
}
