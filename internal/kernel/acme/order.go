package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

const (
	StatusPending = "pending"
	StatusReady   = "ready"
	StatusValid   = "valid"
	StatusInvalid = "invalid"
)

func (c *Client) Register(ctx context.Context, contact string) (Account, error) {
	if err := c.load(ctx); err != nil {
		return Account{}, err
	}
	if c.key == nil {
		key, err := NewKey()
		if err != nil {
			return Account{}, err
		}
		if err := c.UseAccount(Account{Key: key}); err != nil {
			return Account{}, err
		}
	}

	payload := map[string]any{"termsOfServiceAgreed": true}
	if contact != "" {
		payload["contact"] = []string{"mailto:" + contact}
	}

	res, _, err := c.post(ctx, c.index.NewAccount, payload)
	if err != nil {
		return Account{}, err
	}

	url := res.Header.Get("Location")
	if url == "" {
		return Account{}, errors.New("the directory registered no account url")
	}
	c.kid = url

	raw, err := x509.MarshalECPrivateKey(c.key)
	if err != nil {
		return Account{}, fmt.Errorf("marshal the account key: %w", err)
	}

	return Account{
		Key: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})),
		URL: url,
	}, nil
}

func (c *Client) Order(ctx context.Context, names []string) (Order, error) {
	if err := c.load(ctx); err != nil {
		return Order{}, err
	}

	identifiers := make([]map[string]string, 0, len(names))
	for _, name := range names {
		identifiers = append(identifiers, map[string]string{"type": "dns", "value": name})
	}

	res, body, err := c.post(ctx, c.index.NewOrder,
		map[string]any{"identifiers": identifiers})
	if err != nil {
		return Order{}, err
	}

	held, err := decodeOrder(body)
	if err != nil {
		return Order{}, err
	}
	held.URL = res.Header.Get("Location")
	if held.URL == "" {
		return Order{}, errors.New("the directory gave the order no url")
	}

	for _, authorization := range held.authorizations {
		challenge, err := c.challengeIn(ctx, authorization)
		if err != nil {
			return Order{}, err
		}
		held.Challenges = append(held.Challenges, challenge)
	}
	return held.Order, nil
}

type wireOrder struct {
	Order
	authorizations []string
}

func decodeOrder(body []byte) (wireOrder, error) {
	var wire struct {
		Status         string   `json:"status"`
		Finalize       string   `json:"finalize"`
		Certificate    string   `json:"certificate"`
		Authorizations []string `json:"authorizations"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return wireOrder{}, fmt.Errorf("decode the order: %w", err)
	}

	return wireOrder{
		Order: Order{
			Status:      wire.Status,
			Finalize:    wire.Finalize,
			Certificate: wire.Certificate,
		},
		authorizations: wire.Authorizations,
	}, nil
}

func (c *Client) challengeIn(ctx context.Context, url string) (Challenge, error) {
	_, body, err := c.post(ctx, url, nil)
	if err != nil {
		return Challenge{}, err
	}

	var wire struct {
		Identifier struct {
			Value string `json:"value"`
		} `json:"identifier"`
		Challenges []struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"challenges"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return Challenge{}, fmt.Errorf("decode the authorization: %w", err)
	}

	for _, one := range wire.Challenges {
		if one.Type != "http-01" {
			continue
		}
		return Challenge{
			Name:  wire.Identifier.Value,
			Token: one.Token,
			URL:   one.URL,
		}, nil
	}
	return Challenge{}, fmt.Errorf(
		"%s offers no http-01 challenge, and this platform answers no other kind",
		wire.Identifier.Value)
}

func (c *Client) KeyAuthorization(token string) (string, error) {
	if c.key == nil {
		return "", errors.New("this client holds no account key")
	}

	sum, err := thumbprint(&c.key.PublicKey)
	if err != nil {
		return "", err
	}
	return token + "." + sum, nil
}

func (c *Client) Accept(ctx context.Context, challengeURL string) error {
	_, _, err := c.post(ctx, challengeURL, struct{}{})
	return err
}

func (c *Client) Refresh(ctx context.Context, orderURL string) (Order, error) {
	_, body, err := c.post(ctx, orderURL, nil)
	if err != nil {
		return Order{}, err
	}

	held, err := decodeOrder(body)
	if err != nil {
		return Order{}, err
	}
	held.URL = orderURL
	return held.Order, nil
}

func (c *Client) Finalize(ctx context.Context, held Order, names []string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate the certificate key: %w", err)
	}

	template := x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: names[0]},
		DNSNames:           names,
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &template, key)
	if err != nil {
		return "", "", fmt.Errorf("build the certificate request: %w", err)
	}

	if _, _, err := c.post(ctx, held.Finalize,
		map[string]any{"csr": encode(csr)}); err != nil {
		return "", "", err
	}

	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshal the certificate key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})), held.URL, nil
}

func (c *Client) Download(ctx context.Context, certificateURL string) (string, error) {
	_, body, err := c.post(ctx, certificateURL, nil)
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", errors.New("the directory handed back an empty certificate")
	}
	return string(body), nil
}

func (c *Client) Poll(
	ctx context.Context, orderURL string, every, limit time.Duration,
) (Order, error) {
	deadline := time.Now().Add(limit)

	for {
		held, err := c.Refresh(ctx, orderURL)
		if err != nil {
			return Order{}, err
		}
		if held.Status == StatusValid || held.Status == StatusInvalid {
			return held, nil
		}
		if time.Now().After(deadline) {
			return held, fmt.Errorf(
				"the order was still %s after %s, so nothing was issued", held.Status, limit)
		}

		select {
		case <-ctx.Done():
			return Order{}, ctx.Err()
		case <-time.After(every):
		}
	}
}
