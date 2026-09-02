package repository

import (
	"strings"
	"testing"
)

func TestBranchNameIsStableValidAndIdentityScoped(t *testing.T) {
	t.Parallel()
	first, err := BranchName("DAR-68 — Repository Manager", "work_01alpha")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := BranchName("DAR-68 — Repository Manager", "work_01alpha")
	if err != nil {
		t.Fatal(err)
	}
	second, err := BranchName("DAR-68 — Repository Manager", "work_02beta")
	if err != nil {
		t.Fatal(err)
	}
	if first != repeated {
		t.Fatalf("repeated branch = %q, want %q", repeated, first)
	}
	if first == second {
		t.Fatalf("different work items produced the same branch %q", first)
	}
	if !strings.HasPrefix(first, "darkstar/dar-68-repository-manager-") || strings.ContainsAny(first, " \\~^:?*[") {
		t.Fatalf("branch = %q, want normalized darkstar branch", first)
	}
}

func TestBranchNameRequiresStableInputsAndBoundsComponent(t *testing.T) {
	t.Parallel()
	if _, err := BranchName("", "work_1"); err == nil {
		t.Fatal("BranchName() error = nil, want missing source rejection")
	}
	branch, err := BranchName(strings.Repeat("long title ", 30), "work_1")
	if err != nil {
		t.Fatal(err)
	}
	component := strings.TrimPrefix(branch, "darkstar/")
	if len(component) > maxBranchComponentLength {
		t.Fatalf("branch component length = %d, want <= %d", len(component), maxBranchComponentLength)
	}
	ascii, err := BranchName("🚀 運送", "work_1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ascii, "darkstar/work-") {
		t.Fatalf("non-ASCII source branch = %q, want safe fallback", ascii)
	}
}
