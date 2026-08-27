package sealed

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
)

type Keyring struct {
	active *Key
	byID   map[string]Key
}

func NewKeyring(keys []Key) *Keyring {
	ring := &Keyring{byID: make(map[string]Key, len(keys))}
	for i, k := range keys {
		ring.byID[k.ID()] = k
		if i == 0 {
			first := k
			ring.active = &first
		}
	}
	return ring
}

func (r *Keyring) Sealing() bool {
	return r != nil && r.active != nil
}

func (r *Keyring) ActiveID() string {
	if !r.Sealing() {
		return ""
	}
	return r.active.ID()
}

func (r *Keyring) Active() (Key, bool) {
	if !r.Sealing() {
		return Key{}, false
	}
	return *r.active, true
}

func (r *Keyring) Find(id string) (Key, bool) {
	if r == nil {
		return Key{}, false
	}
	k, ok := r.byID[id]
	return k, ok
}

func SealBytes(plaintext []byte, k Key) (string, error) {
	var out bytes.Buffer
	if _, err := Seal(&out, bytes.NewReader(plaintext), k); err != nil {
		return "", err
	}
	return hex.EncodeToString(out.Bytes()), nil
}

func OpenBytes(text string, k Key) ([]byte, error) {
	raw, err := hex.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("the sealed value is not hex: %w", err)
	}

	reader, err := Open(bytes.NewReader(raw), k)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}
