package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultRegion = "us-east-1"
	callTimeout   = 30 * time.Minute
	maxErrorBody  = 8 * 1024
)

var ErrNotFound = errors.New("no such object")

type Config struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
}

type Client struct {
	Region    string
	AccessKey string
	SecretKey string

	base   *url.URL
	bucket string
	http   *http.Client
	now    func() time.Time
}

func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("an object store needs an endpoint")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("an object store needs a bucket")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("an object store needs an access key and a secret key")
	}

	endpoint := cfg.Endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}

	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("read the endpoint: %w", err)
	}
	if base.Host == "" {
		return nil, errors.New("the endpoint has no host")
	}

	region := cfg.Region
	if region == "" {
		region = DefaultRegion
	}

	return &Client{
		Region:    region,
		AccessKey: cfg.AccessKey,
		SecretKey: cfg.SecretKey,
		base:      base,
		bucket:    cfg.Bucket,
		http:      &http.Client{Timeout: callTimeout},
		now:       func() time.Time { return time.Now().UTC() },
	}, nil
}

func (c *Client) Bucket() string {
	return c.bucket
}

func (c *Client) Endpoint() string {
	return c.base.String()
}

func (c *Client) urlFor(key string) string {
	target := *c.base
	target.Path = "/" + c.bucket
	if key != "" {
		target.Path += "/" + key
	}
	return target.String()
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	c.sign(req, c.now())

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach the object store: %w", err)
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return nil, ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		defer res.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		return nil, fmt.Errorf("the object store answered %d: %s",
			res.StatusCode, summarise(string(detail)))
	}
	return res, nil
}

func summarise(body string) string {
	if code := between(body, "<Code>", "</Code>"); code != "" {
		if message := between(body, "<Message>", "</Message>"); message != "" {
			return code + ": " + message
		}
		return code
	}

	body = strings.TrimSpace(body)
	if len(body) > 200 {
		return body[:200]
	}
	if body == "" {
		return "no detail"
	}
	return body
}

func between(body, open, shut string) string {
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	rest := body[start+len(open):]

	end := strings.Index(rest, shut)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func (c *Client) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.urlFor(""), nil)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}

	res, err := c.do(req)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("bucket %s does not exist on %s", c.bucket, c.base.Host)
	}
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.urlFor(key), body)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	res, err := c.do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.urlFor(key), nil)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}

	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.urlFor(key), nil)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}

	res, err := c.do(req)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}
