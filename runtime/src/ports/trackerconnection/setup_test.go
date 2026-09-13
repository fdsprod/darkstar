package trackerconnection

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConnectionCodecRejectsUnknownOrUnpinnedVariants(t *testing.T) {
	record := Record{SchemaVersion: 1, ConnectionID: "connection", Revision: "1", Configuration: GitHubTokenConfiguration{Host: "github.com", CredentialRef: "ref", AccountID: "account-id"}, AccountName: "user", ObservedAt: time.Now().UTC(), EvidenceRef: "retained-evidence"}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		strings.Replace(string(encoded), `"schemaVersion":1`, `"schemaVersion":2`, 1),
		strings.Replace(string(encoded), `"kind":"github_token"`, `"kind":"future-provider"`, 1),
		strings.Replace(string(encoded), `"accountId":"account-id"`, `"accountId":""`, 1),
		strings.Replace(string(encoded), `"host":"github.com"`, `"host":"github.com","login":"wrong-variant"`, 1),
	} {
		var decoded Record
		if err := json.Unmarshal([]byte(content), &decoded); err == nil {
			t.Fatal("accepted an unsupported or unpinned stored connection variant")
		}
	}
}
