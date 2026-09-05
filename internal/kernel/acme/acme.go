package acme

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	LetsEncrypt        = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

	maxBody   = 1 << 20
	callLimit = 30 * time.Second
)

type Account struct {
	Key string
	URL string
}

type Challenge struct {
	Name  string
	Token string
	URL   string
}

type Order struct {
	URL         string
	Status      string
	Finalize    string
	Certificate string
	Challenges  []Challenge
}

type directory struct {
	NewNonce   string `json:"newNonce"`
	NewAccount string `json:"newAccount"`
	NewOrder   string `json:"newOrder"`
}

type Client struct {
	endpoint string
	http     *http.Client

	key   *ecdsa.PrivateKey
	kid   string
	nonce string
	index directory
}

func New(endpoint string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: callLimit}
	}
	return &Client{endpoint: endpoint, http: client}
}

func NewKey() (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate an account key: %w", err)
	}

	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal the account key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})), nil
}

func (c *Client) UseAccount(held Account) error {
	block, _ := pem.Decode([]byte(held.Key))
	if block == nil {
		return errors.New("the account key is not PEM")
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse the account key: %w", err)
	}

	c.key = key
	c.kid = held.URL
	return nil
}

func (c *Client) load(ctx context.Context) error {
	if c.index.NewOrder != "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return fmt.Errorf("build the directory request: %w", err)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch the directory: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the directory answered %d", res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&c.index); err != nil {
		return fmt.Errorf("decode the directory: %w", err)
	}
	if c.index.NewOrder == "" || c.index.NewAccount == "" || c.index.NewNonce == "" {
		return errors.New("the directory is missing newNonce, newAccount or newOrder")
	}
	return nil
}

func (c *Client) freshNonce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.index.NewNonce, nil)
	if err != nil {
		return fmt.Errorf("build the nonce request: %w", err)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch a nonce: %w", err)
	}
	defer res.Body.Close()

	c.nonce = res.Header.Get("Replay-Nonce")
	if c.nonce == "" {
		return errors.New("the directory handed out no nonce")
	}
	return nil
}

func (c *Client) post(ctx context.Context, url string, payload any) (*http.Response, []byte, error) {
	if err := c.load(ctx); err != nil {
		return nil, nil, err
	}
	if c.nonce == "" {
		if err := c.freshNonce(ctx); err != nil {
			return nil, nil, err
		}
	}

	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("encode the payload: %w", err)
		}
		body = encoded
	}

	signed, err := c.sign(url, c.nonce, body)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(signed))
	if err != nil {
		return nil, nil, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/jose+json")

	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("call the directory: %w", err)
	}

	answer, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	res.Body.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("read the answer: %w", err)
	}

	if next := res.Header.Get("Replay-Nonce"); next != "" {
		c.nonce = next
	} else {
		c.nonce = ""
	}

	if res.StatusCode >= http.StatusBadRequest {
		return res, answer, fault(res.StatusCode, answer)
	}
	return res, answer, nil
}

func fault(status int, body []byte) error {
	var problem struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &problem)

	if problem.Detail != "" {
		return fmt.Errorf("%s (%d)", problem.Detail, status)
	}
	return fmt.Errorf("the directory answered %d: %s", status,
		strings.TrimSpace(string(body)))
}
