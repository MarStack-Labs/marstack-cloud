//go:build linux

package container

import (
	"strings"
	"testing"
)

func TestARequestedValueReplacesTheImageOne(t *testing.T) {
	got := mergeEnv([]string{"PATH=/usr/bin", "PORT=80"}, map[string]string{"PORT": "8080"})

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
	got := mergeEnv(image, nil)

	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Fatalf("env = %v, want the image's own environment untouched", got)
	}
}

func TestTheMergeIsOrdered(t *testing.T) {
	first := mergeEnv(nil, map[string]string{"B": "2", "A": "1", "C": "3"})
	second := mergeEnv(nil, map[string]string{"C": "3", "A": "1", "B": "2"})

	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("%v and %v differ: map order would make a restart look like a change",
			first, second)
	}
}
