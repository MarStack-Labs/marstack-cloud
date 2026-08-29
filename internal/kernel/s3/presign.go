package s3

import (
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	MaxPresignWindow = 7 * 24 * time.Hour
	credentialParam  = "X-Amz-Credential"
	signatureParam   = "X-Amz-Signature"
)

func (c *Client) Presign(method, key string, window time.Duration) (string, error) {
	switch method {
	case http.MethodGet, http.MethodPut:
	default:
		return "", errors.New("only GET and PUT are presigned")
	}
	if key == "" {
		return "", errors.New("a presigned url needs a key")
	}
	if window <= 0 || window > MaxPresignWindow {
		return "", errors.New("the window must be positive and at most a week")
	}

	target, err := url.Parse(c.urlFor(key))
	if err != nil {
		return "", err
	}

	at := c.now().UTC()
	stamp := at.Format(stampFormat)
	day := at.Format(dayFormat)
	scope := strings.Join([]string{day, c.Region, service, "aws4_request"}, "/")

	query := target.Query()
	query.Set("X-Amz-Algorithm", algorithm)
	query.Set(credentialParam, c.AccessKey+"/"+scope)
	query.Set("X-Amz-Date", stamp)
	query.Set("X-Amz-Expires", strconv.Itoa(int(window.Seconds())))
	query.Set("X-Amz-SignedHeaders", "host")
	target.RawQuery = query.Encode()

	canonical := strings.Join([]string{
		method,
		canonicalPath(target),
		canonicalQuery(target),
		"host:" + target.Host + "\n",
		"host",
		unsignedPayload,
	}, "\n")

	toSign := strings.Join([]string{algorithm, stamp, scope, hashOf(canonical)}, "\n")
	signature := hex.EncodeToString(mac(signingKey(c.SecretKey, day, c.Region), toSign))

	query.Set(signatureParam, signature)
	target.RawQuery = query.Encode()
	return target.String(), nil
}
