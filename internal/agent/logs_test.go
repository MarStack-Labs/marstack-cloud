package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPartialLineIsHeldBackUntilItEnds(t *testing.T) {
	lines, consumed := splitLines([]byte("one\ntwo\nthr"), false)

	if len(lines) != 2 {
		t.Fatalf("lines = %v, want the two complete ones", lines)
	}
	if consumed != len("one\ntwo\n") {
		t.Fatalf("consumed = %d, want to stop before the partial line: shipping half a "+
			"line means the other half arrives as a second line and never joins up",
			consumed)
	}
}

func TestNothingIsShippedUntilThereIsAWholeLine(t *testing.T) {
	lines, consumed := splitLines([]byte("still writing"), false)

	if len(lines) != 0 || consumed != 0 {
		t.Fatalf("lines = %v consumed = %d, want to wait", lines, consumed)
	}
}

func TestALineThatNeverEndsIsShippedAnyway(t *testing.T) {
	buffer := []byte(strings.Repeat("x", maxHeldLineSize))
	lines, consumed := splitLines(buffer, true)

	if len(lines) != 1 || consumed != len(buffer) {
		t.Fatalf("lines = %d consumed = %d, want it shipped: a workload printing without "+
			"a newline would otherwise stall its whole log forever", len(lines), consumed)
	}
}

func TestOneReportCarriesAtMostTheCap(t *testing.T) {
	var builder strings.Builder
	for i := range maxShipLines + 50 {
		builder.WriteString("line ")
		builder.WriteByte(byte('0' + i%10))
		builder.WriteByte('\n')
	}

	lines, consumed := splitLines([]byte(builder.String()), false)
	if len(lines) != maxShipLines {
		t.Fatalf("lines = %d, want %d", len(lines), maxShipLines)
	}
	if consumed >= builder.Len() {
		t.Fatal("the whole buffer was consumed while only some of it was shipped, so the " +
			"rest is lost")
	}
}

func TestCarriageReturnsAreTrimmed(t *testing.T) {
	lines, _ := splitLines([]byte("one\r\ntwo\r\n"), false)

	for _, line := range lines {
		if strings.HasSuffix(line, "\r") {
			t.Fatalf("line = %q, want the carriage return gone", line)
		}
	}
}

func newLogAgent(t *testing.T) *Agent {
	t.Helper()
	return &Agent{logOffsets: map[string]int64{}}
}

func writeLog(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "output.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestOnlyWhatIsNewIsRead(t *testing.T) {
	a := newLogAgent(t)
	dir := t.TempDir()
	path := writeLog(t, dir, "one\ntwo\n")

	lines, next, err := a.readSince("i-1", path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	a.keepOffset("i-1", next)

	writeLog(t, dir, "one\ntwo\nthree\n")
	lines, _, err = a.readSince("i-1", path)
	if err != nil {
		t.Fatalf("read again: %v", err)
	}
	if len(lines) != 1 || lines[0] != "three" {
		t.Fatalf("lines = %v, want only the new one: resending is duplicate output in "+
			"somebody's log", lines)
	}
}

func TestATruncatedLogStartsOver(t *testing.T) {
	a := newLogAgent(t)
	dir := t.TempDir()
	path := writeLog(t, dir, "one\ntwo\nthree\nfour\n")

	_, next, _ := a.readSince("i-1", path)
	a.keepOffset("i-1", next)

	writeLog(t, dir, "fresh\n")
	lines, _, err := a.readSince("i-1", path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(lines) != 1 || lines[0] != "fresh" {
		t.Fatalf("lines = %v, want the rotated file read from the start: an offset past "+
			"the end of a shorter file reads nothing, ever again", lines)
	}
}

func TestTheOffsetOnlyMovesOnceTheLinesAreShipped(t *testing.T) {
	a := newLogAgent(t)
	path := writeLog(t, t.TempDir(), "one\ntwo\n")

	first, _, _ := a.readSince("i-1", path)
	second, _, _ := a.readSince("i-1", path)

	if len(first) != len(second) {
		t.Fatalf("first = %v second = %v, want the same lines until the offset is kept: "+
			"advancing before the report lands loses everything a failed request held",
			first, second)
	}
}

func TestAMissingLogIsNotAnError(t *testing.T) {
	a := newLogAgent(t)

	lines, _, err := a.readSince("i-1", filepath.Join(t.TempDir(), "nothing.log"))
	if err != nil || len(lines) != 0 {
		t.Fatalf("lines = %v err = %v, want a quiet nothing: a workload that has not "+
			"printed yet is normal", lines, err)
	}
}

func TestAnInstanceThatIsGoneIsForgotten(t *testing.T) {
	a := newLogAgent(t)
	a.keepOffset("i-old", 128)
	a.keepOffset("i-here", 64)

	a.forgetLogs([]instanceView{{ID: "i-here"}})

	if _, held := a.logOffsets["i-old"]; held {
		t.Fatal("an offset for an instance that is no longer assigned was kept, so the map " +
			"grows for as long as the agent runs")
	}
	if a.logOffsets["i-here"] != 64 {
		t.Fatal("the offset for an instance still here was dropped, so its log resends")
	}
}
