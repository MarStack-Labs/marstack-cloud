package app

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

func callerKey(r *http.Request) string {
	if raw, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		if secret := strings.TrimSpace(raw); secret != "" {
			sum := sha256.Sum256([]byte(secret))
			return "t:" + hex.EncodeToString(sum[:8])
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "a:" + host
}

func openToEveryone(r *http.Request) bool {
	return openPaths[r.URL.Path]
}
