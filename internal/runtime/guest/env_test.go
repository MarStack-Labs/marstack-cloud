package guest

import (
	"strings"
	"testing"
)

func TestARequestedValueReplacesTheImageOne(t *testing.T) {
	got := MergeEnv([]string{"PATH=/usr/bin", "PORT=80"}, map[string]string{"PORT": "8080"})

	joined := strings.Join(got, " ")
	if strings.Contains(joined, "PORT=80 ") || strings.HasSuffix(joined, "PORT=80") {
		t.Fatalf("env = %v, want the image's PORT gone rather than shadowed by ordering", got)
	}
	if !strings.Contains(joined, "PORT=8080") || !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("env = %v, want the override applied and the rest of the image kept", got)
	}
}

func TestNothingRequestedLeavesTheImageAlone(t *testing.T) {
	image := []string{"PATH=/usr/bin"}
	got := MergeEnv(image, nil)

	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Fatalf("env = %v, want the image's own environment untouched", got)
	}
}

func TestTheMergeIsOrdered(t *testing.T) {
	first := MergeEnv(nil, map[string]string{"B": "2", "A": "1", "C": "3"})
	second := MergeEnv(nil, map[string]string{"C": "3", "A": "1", "B": "2"})

	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("%v and %v differ: map order would make a restart look like a change",
			first, second)
	}
}

func TestTheMergeIsWhatBothRuntimesUse(t *testing.T) {
	got := MergeEnv([]string{"PATH=/usr/bin"}, map[string]string{"ROLE": "worker"})

	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "ROLE=worker") {
		t.Fatalf("env = %v, want the requested variable: a runtime that only exports the "+
			"image's environment makes --env a silent no-op", got)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("env = %v, want the image's own kept", got)
	}
}
