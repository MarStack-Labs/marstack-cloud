package keypair

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

var kinds = map[string]bool{
	"ssh-ed25519":                        true,
	"ssh-rsa":                            true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

type parsed struct {
	Kind        string
	Body        string
	Comment     string
	Fingerprint string
}

func parsePublicKey(text string) (parsed, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return parsed{}, fmt.Errorf("the key is empty")
	}
	if strings.HasPrefix(text, "-----BEGIN") {
		return parsed{}, fmt.Errorf("that looks like a private key; give the .pub file instead")
	}
	if len(text) > MaxKeyBytes {
		return parsed{}, fmt.Errorf("the key is longer than %d bytes", MaxKeyBytes)
	}
	if strings.ContainsAny(text, "\n\r") {
		return parsed{}, fmt.Errorf("give one key, on one line")
	}

	fields := strings.Fields(text)
	if len(fields) < 2 {
		return parsed{}, fmt.Errorf("a key looks like \"<type> <base64> [comment]\"")
	}
	if !kinds[fields[0]] {
		return parsed{}, fmt.Errorf("%q is not a key type this platform accepts", fields[0])
	}

	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return parsed{}, fmt.Errorf("the key body is not base64: %w", err)
	}
	if len(blob) == 0 {
		return parsed{}, fmt.Errorf("the key body is empty")
	}

	sum := sha256.Sum256(blob)
	return parsed{
		Kind:        fields[0],
		Body:        fields[1],
		Comment:     strings.Join(fields[2:], " "),
		Fingerprint: "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "="),
	}, nil
}

func (p parsed) line() string {
	line := p.Kind + " " + p.Body
	if p.Comment != "" {
		line += " " + p.Comment
	}
	return line
}
