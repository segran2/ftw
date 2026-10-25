package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mpc.Service.Replan labels its plan "manual", the operator's request: the Plan
// page shows "replanned 1m ago (manual)" and the support report gives it as the
// trigger. Only POST /api/mpc/replan in internal/api may use it. A replan Core
// starts by itself names its cause.
//
// On 25 Oct 2026 a fuse-saturation trigger in this package (since removed)
// replanned three times in three minutes on the home box as "manual" while
// nobody touched it.
var unnamedReplan = regexp.MustCompile(`\.Replan\(`)

func TestCoreReplansNameTheirCause(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("found %d Go files; run this test from cmd/ftw", len(files))
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if unnamedReplan.MatchString(line) {
				t.Errorf("%s:%d replans as \"manual\"; pass the cause to ReplanWithReason: %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
