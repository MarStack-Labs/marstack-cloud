package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func Pool(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}

	pem, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read the certificate authority: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return pool, nil
}

func Client(path string) (*tls.Config, error) {
	pool, err := Pool(path)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, nil
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func Server(certFile, keyFile string) (*tls.Config, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load the server certificate: %w", err)
	}

	return &tls.Config{
		Certificates:     []tls.Certificate{pair},
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
	}, nil
}

const MaxPEMBytes = 128 * 1024

type Summary struct {
	Subject   string
	ExpiresAt string
}

func Inspect(certPEM, keyPEM string) (Summary, error) {
	if len(certPEM) > MaxPEMBytes || len(keyPEM) > MaxPEMBytes {
		return Summary{}, errors.New("a certificate or key longer than 128 KiB is not one")
	}

	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return Summary{}, fmt.Errorf("the certificate and key are not a usable pair: %w", err)
	}

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Summary{}, fmt.Errorf("the leaf certificate does not parse: %w", err)
	}

	return Summary{
		Subject:   leaf.Subject.CommonName,
		ExpiresAt: leaf.NotAfter.UTC().Format(time.RFC3339),
	}, nil
}

func Bundle(certPEM, keyPEM string) string {
	return strings.TrimRight(certPEM, "\n") + "\n" + strings.TrimRight(keyPEM, "\n") + "\n"
}

func Split(bundled string) (string, string) {
	var chain, key strings.Builder

	rest := []byte(bundled)
	for {
		block, remainder := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remainder

		encoded := pem.EncodeToMemory(block)
		if block.Type == "CERTIFICATE" {
			chain.Write(encoded)
			continue
		}
		key.Write(encoded)
	}
	return chain.String(), key.String()
}
