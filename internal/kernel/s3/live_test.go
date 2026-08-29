package s3

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveClient(t *testing.T) *Client {
	t.Helper()

	endpoint := os.Getenv("MARSTACK_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MARSTACK_S3_TEST_ENDPOINT to run against a real object store")
	}

	c, err := New(Config{
		Endpoint:  endpoint,
		Bucket:    os.Getenv("MARSTACK_S3_TEST_BUCKET"),
		AccessKey: os.Getenv("MARSTACK_S3_TEST_ACCESS_KEY"),
		SecretKey: os.Getenv("MARSTACK_S3_TEST_SECRET_KEY"),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func TestLiveSignatureIsAcceptedByARealStore(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	if err := c.Check(ctx); err != nil {
		t.Fatalf("the store refused a signed HEAD: %v", err)
	}

	key := "probe/round-trip"
	if err := c.Put(ctx, key, strings.NewReader("hello from marstack"), 19); err != nil {
		t.Fatalf("put: %v", err)
	}

	body, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	back, _ := io.ReadAll(body)
	body.Close()

	if string(back) != "hello from marstack" {
		t.Fatalf("got %q back", back)
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestLiveAPresignedURLWorksWithNoCredentials(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	key := "probe/presigned"

	put, err := c.Presign(http.MethodPut, key, 15*time.Minute)
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, put,
		strings.NewReader("written with no credentials"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req.ContentLength = 27

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put through the presigned url: %v", err)
	}
	detail, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		t.Fatalf("the store refused the presigned PUT: %d %s", res.StatusCode, detail)
	}

	get, err := c.Presign(http.MethodGet, key, 15*time.Minute)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}

	res, err = http.Get(get)
	if err != nil {
		t.Fatalf("get through the presigned url: %v", err)
	}
	back, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("the store refused the presigned GET: %d %s", res.StatusCode, back)
	}
	if string(back) != "written with no credentials" {
		t.Fatalf("got %q back", back)
	}

	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestLiveAPresignedURLIsUselessForAnotherObject(t *testing.T) {
	c := liveClient(t)

	get, err := c.Presign(http.MethodGet, "probe/one", 15*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	stolen := strings.Replace(get, "probe/one", "probe/two", 1)

	res, err := http.Get(stolen)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	res.Body.Close()

	if res.StatusCode < 400 {
		t.Fatalf("status = %d: a url for one object also read another, so the signature "+
			"does not cover the key", res.StatusCode)
	}
}
