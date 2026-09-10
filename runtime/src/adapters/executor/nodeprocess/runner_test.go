package nodeprocess

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunnerUsesExplicitDirectoryEnvironmentAndPreservesArgumentBoundaries(t *testing.T) {
	root := t.TempDir()
	r := Runner{Environment: append(os.Environ(), "DARKSTAR_NODE_RUNNER_TEST=1"), OutputLimit: 65536}
	output, err := r.Run(t.Context(), root, []string{os.Args[0], "-test.run=TestRunnerHelper", "--", "argument with spaces"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, root) || !strings.Contains(output, "argument with spaces") {
		t.Fatalf("output = %q", output)
	}
	r.OutputLimit = 4
	output, err = r.Run(t.Context(), root, []string{os.Args[0], "-test.run=TestRunnerHelper", "--", "fail"}, time.Minute)
	if err == nil || len(output) != 4 {
		t.Fatalf("exit or output limit lost: %q %v", output, err)
	}
}

func TestRunnerHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (Runner{}).Run(ctx, t.TempDir(), []string{os.Args[0], "-test.run=TestRunnerHelper"}, time.Minute)
	if err == nil {
		t.Fatal("cancelled command succeeded")
	}
}

func TestRunnerHelper(t *testing.T) {
	if os.Getenv("DARKSTAR_NODE_RUNNER_TEST") != "1" {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(dir)
	arg := os.Args[len(os.Args)-1]
	fmt.Println(arg)
	if arg == "fail" {
		os.Exit(3)
	}
	os.Exit(0)
}
