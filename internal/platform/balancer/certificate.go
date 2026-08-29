package balancer

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

const MaxCertificateBytes = 128 * 1024

func sealCertificate(certPEM, keyPEM string, keys *sealed.Keyring) (TLS, error) {
	if len(certPEM) > MaxCertificateBytes || len(keyPEM) > MaxCertificateBytes {
		return TLS{}, fault.Invalid("invalid_certificate",
			"a certificate or key longer than 128 KiB is not one")
	}

	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return TLS{}, fault.Invalid("invalid_certificate",
			"the certificate and key are not a usable pair: "+err.Error())
	}

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return TLS{}, fault.Invalid("invalid_certificate",
			"the leaf certificate does not parse: "+err.Error())
	}

	active, sealing := keys.Active()
	if !sealing {
		return TLS{}, fault.Conflict("no_sealing_key",
			"this control plane has no key to seal a private key with, and handing one to "+
				"every node from a database it cannot protect is not something it will do "+
				"quietly: start it with --backup-key-file")
	}

	blob, err := sealed.SealBytes([]byte(bundle(certPEM, keyPEM)), active)
	if err != nil {
		return TLS{}, fault.Internal(fmt.Errorf("seal the certificate: %w", err))
	}

	return TLS{
		Material:  blob,
		KeyID:     keys.ActiveID(),
		Subject:   leaf.Subject.CommonName,
		ExpiresAt: leaf.NotAfter.UTC().Format(time.RFC3339),
	}, nil
}

func openCertificate(t TLS, keys *sealed.Keyring) (string, string, error) {
	if !t.Present() {
		return "", "", nil
	}

	k, held := keys.Find(t.KeyID)
	if !held {
		return "", "", fault.Conflict("seal_key_missing",
			"this balancer's certificate was sealed with key "+t.KeyID+
				", which this control plane does not hold")
	}

	plain, err := sealed.OpenBytes(t.Material, k)
	if err != nil {
		return "", "", fault.Internal(fmt.Errorf("unseal the certificate: %w", err))
	}

	certPEM, keyPEM := split(string(plain))
	if certPEM == "" || keyPEM == "" {
		return "", "", fault.Internal(errors.New(
			"the sealed bundle holds no certificate or no key"))
	}
	return certPEM, keyPEM, nil
}

func bundle(certPEM, keyPEM string) string {
	return strings.TrimRight(certPEM, "\n") + "\n" + strings.TrimRight(keyPEM, "\n") + "\n"
}

func split(bundled string) (string, string) {
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
