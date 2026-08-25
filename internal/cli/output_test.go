package cli

import (
	"strings"
	"testing"
)

func TestInstanceRowShowsTheFailureReason(t *testing.T) {
	row := instanceRow(instanceView{
		Name:            "broken-1",
		ID:              "i-abc",
		Observed:        "failed",
		ObservedMessage: "image nginx:1.27 is not present on this node",
	})

	joined := strings.Join(row, " ")
	if !strings.Contains(joined, "not present on this node") {
		t.Fatalf("row = %q, want it to carry the failure reason", joined)
	}
}

func TestInstanceRowTruncatesLongMessages(t *testing.T) {
	row := instanceRow(instanceView{
		ObservedMessage: strings.Repeat("x", 500),
	})

	for _, cell := range row {
		if len(cell) > 60 {
			t.Fatalf("cell is %d characters, want the table to stay readable", len(cell))
		}
	}
}

func TestInstanceRowHasOneCellPerHeader(t *testing.T) {
	row := instanceRow(instanceView{Name: "api-1"})

	if len(row) != len(instanceHeaders) {
		t.Fatalf("row has %d cells for %d headers", len(row), len(instanceHeaders))
	}
}

func TestNodeRowHasOneCellPerHeader(t *testing.T) {
	row := nodeRow(nodeView{Name: "bm-1"})

	if len(row) != len(nodeHeaders) {
		t.Fatalf("row has %d cells for %d headers", len(row), len(nodeHeaders))
	}
}
