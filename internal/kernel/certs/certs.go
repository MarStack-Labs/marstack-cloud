package certs

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
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
