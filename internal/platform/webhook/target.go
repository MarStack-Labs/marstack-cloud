package webhook

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func checkURL(raw string) (string, error) {
	if len(raw) > MaxURLLength {
		return "", errors.New("the url is too long")
	}

	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("that is not a url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("the scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("the url has no host")
	}
	if parsed.User != nil {
		return "", errors.New("credentials in the url are not carried, so leave them out")
	}
	return parsed.String(), nil
}

func reachable(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("could not read the address")
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("could not read the address")
	}

	switch {
	case ip.IsLoopback():
		return errors.New("refusing to post to loopback: that is the control plane itself")
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return errors.New("refusing to post to a link-local address: that is where cloud " +
			"metadata lives")
	case ip.IsUnspecified(), ip.IsMulticast():
		return errors.New("refusing to post to that address")
	}
	return nil
}

type guard func(address string) error

func newClient(allow guard) *http.Client {
	dialer := &net.Dialer{Timeout: DeliverTimeout}
	if allow == nil {
		allow = reachable
	}

	return &http.Client{
		Timeout: DeliverTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the target redirected, which is not followed")
		},
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if err := allow(address); err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, address)
			},
			TLSHandshakeTimeout:   DeliverTimeout,
			ResponseHeaderTimeout: DeliverTimeout,
			ExpectContinueTimeout: time.Second,
		},
	}
}
