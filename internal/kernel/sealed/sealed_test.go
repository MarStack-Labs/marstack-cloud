package sealed

import (
	"bytes"
	"crypto/rand"
	"io"
	"strings"
	"testing"
)

func plaintext(t *testing.T, size int) []byte {
	t.Helper()

	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("draw plaintext: %v", err)
	}
	return raw
}

func roundTrip(t *testing.T, want []byte, k Key) {
	t.Helper()

	var sealedBytes bytes.Buffer
	written, err := Seal(&sealedBytes, bytes.NewReader(want), k)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if written != int64(len(want)) {
		t.Fatalf("wrote %d, want %d", written, len(want))
	}

	reader, err := Open(bytes.NewReader(sealedBytes.Bytes()), k)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read back %d bytes, want %d", len(got), len(want))
	}
}

func TestEveryLengthAroundAFrameBoundaryRoundTrips(t *testing.T) {
	k, _ := NewKey()

	for _, size := range []int{
		0, 1, 15, 16, 17,
		FrameSize - 1, FrameSize, FrameSize + 1,
		2*FrameSize - 1, 2 * FrameSize, 2*FrameSize + 1,
		5*FrameSize + 1234,
	} {
		roundTrip(t, plaintext(t, size), k)
	}
}

func TestTheSealedFormIsNotThePlaintext(t *testing.T) {
	k, _ := NewKey()
	want := bytes.Repeat([]byte("secret volume bytes "), 4096)

	var out bytes.Buffer
	if _, err := Seal(&out, bytes.NewReader(want), k); err != nil {
		t.Fatalf("seal: %v", err)
	}

	if bytes.Contains(out.Bytes(), []byte("secret volume bytes")) {
		t.Fatal("the plaintext is readable in the sealed stream")
	}
	if out.Len() <= len(want) {
		t.Fatalf("sealed %d bytes from %d, want the tags to be there", out.Len(), len(want))
	}
}

func TestTwoSealsOfTheSameBytesDiffer(t *testing.T) {
	k, _ := NewKey()
	want := plaintext(t, 4096)

	var first, second bytes.Buffer
	if _, err := Seal(&first, bytes.NewReader(want), k); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Seal(&second, bytes.NewReader(want), k); err != nil {
		t.Fatalf("seal: %v", err)
	}

	if bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("two seals produced identical bytes, so the salt is not doing its job and " +
			"an observer learns when two backups hold the same data")
	}
}

func TestAnotherKeyCannotOpenIt(t *testing.T) {
	k, _ := NewKey()
	other, _ := NewKey()

	var out bytes.Buffer
	if _, err := Seal(&out, bytes.NewReader(plaintext(t, 4096)), k); err != nil {
		t.Fatalf("seal: %v", err)
	}

	reader, err := Open(bytes.NewReader(out.Bytes()), other)
	if err != nil {
		return
	}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("the wrong key read the stream")
	}
}

func TestATruncatedStreamIsRefusedRatherThanShortened(t *testing.T) {
	k, _ := NewKey()
	want := plaintext(t, 3*FrameSize)

	var out bytes.Buffer
	if _, err := Seal(&out, bytes.NewReader(want), k); err != nil {
		t.Fatalf("seal: %v", err)
	}

	for _, cut := range []int{1, tagBytes + 1, FrameSize, 2 * (FrameSize + tagBytes)} {
		body := out.Bytes()
		if cut >= len(body) {
			continue
		}

		reader, err := Open(bytes.NewReader(body[:len(body)-cut]), k)
		if err != nil {
			continue
		}

		got, err := io.ReadAll(reader)
		if err == nil {
			t.Fatalf("cutting %d bytes returned %d bytes with no error, so a half-copied "+
				"backup would restore as a silently short disk", cut, len(got))
		}
	}
}

func TestAppendedBytesAreRefused(t *testing.T) {
	k, _ := NewKey()

	for _, size := range []int{0, 1024, FrameSize, FrameSize + 1, 2 * FrameSize} {
		var out bytes.Buffer
		if _, err := Seal(&out, bytes.NewReader(plaintext(t, size)), k); err != nil {
			t.Fatalf("seal: %v", err)
		}

		body := append(out.Bytes(), plaintext(t, 40)...)
		reader, err := Open(bytes.NewReader(body), k)
		if err != nil {
			continue
		}

		if _, err := io.ReadAll(reader); err == nil {
			t.Fatalf("appending to a %d byte stream went unnoticed, so a backup could "+
				"grow bytes nobody wrote", size)
		}
	}
}

func TestFlippingAnyByteIsCaught(t *testing.T) {
	k, _ := NewKey()

	var out bytes.Buffer
	if _, err := Seal(&out, bytes.NewReader(plaintext(t, 2*FrameSize+7)), k); err != nil {
		t.Fatalf("seal: %v", err)
	}

	body := out.Bytes()
	for _, at := range []int{0, 3, 4, 5, 20, len(magic) + saltBytes + 10,
		FrameSize, len(body) - 1} {
		if at >= len(body) {
			continue
		}

		broken := bytes.Clone(body)
		broken[at] ^= 0x01

		reader, err := Open(bytes.NewReader(broken), k)
		if err != nil {
			continue
		}
		if _, err := io.ReadAll(reader); err == nil {
			t.Fatalf("flipping byte %d went unnoticed", at)
		}
	}
}

func TestFramesFromAnotherStreamCannotBeSpliced(t *testing.T) {
	k, _ := NewKey()

	var first, second bytes.Buffer
	if _, err := Seal(&first, bytes.NewReader(plaintext(t, 2*FrameSize)), k); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Seal(&second, bytes.NewReader(plaintext(t, 2*FrameSize)), k); err != nil {
		t.Fatalf("seal: %v", err)
	}

	head := len(magic) + 1 + saltBytes
	frame := FrameSize + tagBytes

	spliced := bytes.Clone(first.Bytes())
	copy(spliced[head:head+frame], second.Bytes()[head:head+frame])

	reader, err := Open(bytes.NewReader(spliced), k)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("a frame taken from another stream under the same key was accepted")
	}
}

func TestAKeyIsReadFromHexAndFingerprinted(t *testing.T) {
	k, text := NewKey()
	if len(text) != KeyBytes*2 {
		t.Fatalf("text is %d characters, want %d", len(text), KeyBytes*2)
	}

	parsed, err := ParseKey("  " + text + "\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.ID() != k.ID() {
		t.Fatal("the same key produced two fingerprints")
	}

	other, _ := NewKey()
	if other.ID() == k.ID() {
		t.Fatal("two keys share a fingerprint")
	}
}

func TestWhatIsNotAKeyIsRefused(t *testing.T) {
	for name, text := range map[string]string{
		"empty":     "",
		"too short": strings.Repeat("a", 62),
		"too long":  strings.Repeat("a", 66),
		"not hex":   strings.Repeat("z", 64),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseKey(text); err == nil {
				t.Fatalf("%q was accepted as a key", text)
			}
		})
	}
}
