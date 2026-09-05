package acme

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
)

func encode(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func coordinates(key *ecdsa.PublicKey) ([]byte, []byte, error) {
	raw, err := key.Bytes()
	if err != nil {
		return nil, nil, fmt.Errorf("encode the account key: %w", err)
	}
	if len(raw) != 65 || raw[0] != 4 {
		return nil, nil, fmt.Errorf("the account key is not an uncompressed P-256 point")
	}
	return raw[1:33], raw[33:65], nil
}

func jwk(key *ecdsa.PublicKey) (map[string]string, error) {
	x, y, err := coordinates(key)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"crv": "P-256",
		"kty": "EC",
		"x":   encode(x),
		"y":   encode(y),
	}, nil
}

func pad(value *big.Int) []byte {
	out := make([]byte, 32)
	value.FillBytes(out)
	return out
}

func thumbprint(key *ecdsa.PublicKey) (string, error) {
	x, y, err := coordinates(key)
	if err != nil {
		return "", err
	}

	ordered := `{"crv":"P-256","kty":"EC","x":"` + encode(x) + `","y":"` + encode(y) + `"}`
	sum := sha256.Sum256([]byte(ordered))
	return encode(sum[:]), nil
}

func (c *Client) sign(url, nonce string, payload []byte) ([]byte, error) {
	header := map[string]any{"alg": "ES256", "nonce": nonce, "url": url}
	if c.kid != "" {
		header["kid"] = c.kid
	} else {
		held, err := jwk(&c.key.PublicKey)
		if err != nil {
			return nil, err
		}
		header["jwk"] = held
	}

	raw, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("encode the header: %w", err)
	}

	protected := encode(raw)
	body := encode(payload)
	if payload == nil {
		body = ""
	}

	sum := sha256.Sum256([]byte(protected + "." + body))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, sum[:])
	if err != nil {
		return nil, fmt.Errorf("sign the request: %w", err)
	}

	signature := append(pad(r), pad(s)...)
	return json.Marshal(map[string]string{
		"protected": protected,
		"payload":   body,
		"signature": encode(signature),
	})
}
