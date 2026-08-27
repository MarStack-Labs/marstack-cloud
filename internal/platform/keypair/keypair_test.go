package keypair

import (
	"strings"
	"testing"
)

const sample = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIL0l2S7DDA2xhJmCJ8+eVQZlP4kzHJqPrGm0k" +
	"XlbLK9M umar@laptop"

func TestAPublicKeyIsParsedAndFingerprinted(t *testing.T) {
	key, err := parsePublicKey("  " + sample + "\t")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if key.Kind != "ssh-ed25519" {
		t.Fatalf("kind = %q", key.Kind)
	}
	if key.Comment != "umar@laptop" {
		t.Fatalf("comment = %q", key.Comment)
	}
	if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q, want the shape ssh-keygen prints", key.Fingerprint)
	}
	if key.line() != sample {
		t.Fatalf("line = %q, want the key back as given", key.line())
	}
}

func TestTheSameKeyWithADifferentCommentFingerprintsTheSame(t *testing.T) {
	first, err := parsePublicKey(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	second, err := parsePublicKey(strings.Replace(sample, "umar@laptop", "umar@desktop", 1))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if first.Fingerprint != second.Fingerprint {
		t.Fatal("renaming the comment changed the fingerprint, so the same key added twice " +
			"would look like two keys")
	}
}

func TestAPrivateKeyIsRefusedWithAUsefulReason(t *testing.T) {
	_, err := parsePublicKey("-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n")
	if err == nil {
		t.Fatal("a private key was accepted")
	}
	if !strings.Contains(err.Error(), "private key") {
		t.Fatalf("err = %v, want it to say what went wrong", err)
	}
}

func TestWhatIsNotAPublicKeyIsRefused(t *testing.T) {
	for name, text := range map[string]string{
		"empty":        "",
		"no body":      "ssh-ed25519",
		"unknown type": "ssh-dss AAAAB3NzaC1kc3M= x",
		"not base64":   "ssh-ed25519 not-base64!! x",
		"two keys":     sample + "\n" + sample,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePublicKey(text); err == nil {
				t.Fatalf("%q was accepted", text)
			}
		})
	}
}
