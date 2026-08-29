package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm       = "AWS4-HMAC-SHA256"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	service         = "s3"

	stampFormat = "20060102T150405Z"
	dayFormat   = "20060102"
)

func (c *Client) sign(req *http.Request, at time.Time) {
	stamp := at.UTC().Format(stampFormat)
	day := at.UTC().Format(dayFormat)

	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", unsignedPayload)
	if req.Host != "" {
		req.Header.Set("Host", req.Host)
	}

	signed, headers := canonicalHeaders(req)
	canonical := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL),
		headers,
		signed,
		unsignedPayload,
	}, "\n")

	scope := strings.Join([]string{day, c.Region, service, "aws4_request"}, "/")
	toSign := strings.Join([]string{
		algorithm,
		stamp,
		scope,
		hashOf(canonical),
	}, "\n")

	key := signingKey(c.SecretKey, day, c.Region)
	signature := hex.EncodeToString(mac(key, toSign))

	req.Header.Set("Authorization", algorithm+
		" Credential="+c.AccessKey+"/"+scope+
		", SignedHeaders="+signed+
		", Signature="+signature)
}

func canonicalHeaders(req *http.Request) (string, string) {
	names := make([]string, 0, len(req.Header)+1)
	values := map[string]string{}

	for name, held := range req.Header {
		lower := strings.ToLower(name)
		if lower != "host" && lower != "content-length" &&
			!strings.HasPrefix(lower, "x-amz-") && lower != "content-type" {
			continue
		}
		names = append(names, lower)
		values[lower] = strings.TrimSpace(strings.Join(held, ","))
	}

	if _, held := values["host"]; !held {
		names = append(names, "host")
		values["host"] = req.Host
	}
	sort.Strings(names)

	var block strings.Builder
	for _, name := range names {
		block.WriteString(name)
		block.WriteString(":")
		block.WriteString(values[name])
		block.WriteString("\n")
	}
	return strings.Join(names, ";"), block.String()
}

func canonicalPath(target *url.URL) string {
	path := target.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

func canonicalQuery(target *url.URL) string {
	query := target.Query()

	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		held := query[key]
		sort.Strings(held)
		for _, value := range held {
			parts = append(parts, escape(key)+"="+escape(value))
		}
	}
	return strings.Join(parts, "&")
}

func escape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func signingKey(secret, day, region string) []byte {
	key := mac([]byte("AWS4"+secret), day)
	key = mac(key, region)
	key = mac(key, service)
	return mac(key, "aws4_request")
}

func mac(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}

func hashOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
