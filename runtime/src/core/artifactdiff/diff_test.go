package artifactdiff

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGenerateIsStableContextBoundAndPaginated(t *testing.T) {
	left := []byte("zero\none\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\n")
	right := []byte("zero\none\ntwo\nTHREE\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\nthirteen\n")
	first, policy, policyDigest, resultDigest, err := Generate(left, right, "binding", "", 4)
	if err != nil || first.NextCursor == "" || policy.Algorithm != AlgorithmVersion || len(policyDigest) != 64 || len(resultDigest) != 64 {
		t.Fatalf("first page = %#v, %#v, %q, %q, %v", first, policy, policyDigest, resultDigest, err)
	}
	repeated, _, repeatedPolicyDigest, repeatedResultDigest, err := Generate(left, right, "binding", "", 4)
	if err != nil || !reflect.DeepEqual(first, repeated) || policyDigest != repeatedPolicyDigest || resultDigest != repeatedResultDigest {
		t.Fatalf("repeat changed: %#v %#v %v", first, repeated, err)
	}
	second, _, _, secondDigest, err := Generate(left, right, "binding", first.NextCursor, 4)
	if err != nil || secondDigest != resultDigest || second.Total != first.Total {
		t.Fatalf("second page = %#v, %q, %v", second, secondDigest, err)
	}
	if _, _, _, _, err := Generate(left, right, "other-binding", first.NextCursor, 4); err == nil {
		t.Fatal("cursor was accepted for another exact binding")
	}
	if _, _, _, _, err := Generate(left, right, "binding", first.NextCursor, 5); err == nil {
		t.Fatal("cursor was accepted under a different page policy")
	}
	allPages := []Page{first, second}
	for cursor := second.NextCursor; cursor != ""; {
		page, _, _, _, pageErr := Generate(left, right, "binding", cursor, 4)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		allPages = append(allPages, page)
		cursor = page.NextCursor
	}
	foundOmission := false
	for _, page := range allPages {
		for _, hunk := range page.Hunks {
			for _, entry := range hunk.Entries {
				foundOmission = foundOmission || entry.Kind == "omitted"
			}
		}
	}
	if !foundOmission {
		t.Fatal("long unchanged region was not represented explicitly")
	}
}

func TestGenerateUsesFixedWorkAndResponseBudgets(t *testing.T) {
	large := strings.Repeat("a\n", 1001)
	changed := strings.Repeat("b\n", 1001)
	if _, _, _, _, err := Generate([]byte(large), []byte(changed), "binding", "", 100); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("work budget error = %v", err)
	}
	// Quotes are valid UTF-8 and remain below the input cap, but JSON escaping
	// expands this single entry beyond the response cap.
	longLine := strings.Repeat("\"", 1600000)
	if _, _, _, _, err := Generate([]byte(longLine), []byte("short"), "binding", "", 1); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("response budget error = %v", err)
	}
	page, _, _, _, err := Generate([]byte(strings.Repeat("x", 1<<20)), []byte("short"), "binding", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if len(encoded) > MaxPageBytes {
		t.Fatalf("page bytes = %d", len(encoded))
	}
}

func TestEntryWireShapeIsClosedByKind(t *testing.T) {
	for _, invalid := range []string{
		`{"kind":"omitted","fromLine":1,"toLine":1,"omittedLines":2,"text":"leak"}`,
		`{"kind":"added","toLine":1,"omittedLines":2,"text":"x"}`,
		`{"kind":"removed","fromLine":1}`,
		`{"kind":"unknown"}`,
	} {
		var entry Entry
		if err := json.Unmarshal([]byte(invalid), &entry); err == nil {
			t.Fatalf("invalid entry accepted: %s", invalid)
		}
	}
	encoded, err := json.Marshal(Entry{Kind: "added", ToLine: 1, Text: ""})
	if err != nil || string(encoded) != `{"kind":"added","toLine":1,"text":""}` {
		t.Fatalf("empty line encoding = %s, %v", encoded, err)
	}
	for _, invalid := range []Entry{
		{Kind: "added", FromLine: 1, ToLine: 1, Text: "x"},
		{Kind: "removed", Text: "x"},
		{Kind: "omitted", FromLine: 1, ToLine: 1, OmittedLines: 2, Text: "leak"},
	} {
		if _, err := json.Marshal(invalid); err == nil {
			t.Fatalf("invalid entry marshaled: %#v", invalid)
		}
	}
}
