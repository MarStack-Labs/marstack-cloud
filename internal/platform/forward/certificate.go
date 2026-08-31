package forward

import (
	"context"
	"errors"
	"fmt"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/certs"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

func sealCertificate(certPEM, keyPEM string, keys *sealed.Keyring) (TLS, error) {
	summary, err := certs.Inspect(certPEM, keyPEM)
	if err != nil {
		return TLS{}, fault.Invalid("invalid_certificate", err.Error())
	}

	blob, keyID, err := sealed.SealJSON(certs.Bundle(certPEM, keyPEM), keys)
	if errors.Is(err, sealed.ErrNoKey) {
		return TLS{}, fault.Conflict("no_sealing_key",
			"this control plane has no key to seal a private key with, and handing one to "+
				"every node from a database it cannot protect is not something it will do "+
				"quietly: start it with --backup-key-file")
	}
	if err != nil {
		return TLS{}, fault.Internal(fmt.Errorf("seal the certificate: %w", err))
	}

	return TLS{
		Material:  blob,
		KeyID:     keyID,
		Subject:   summary.Subject,
		ExpiresAt: summary.ExpiresAt,
	}, nil
}

func openCertificate(t TLS, keys *sealed.Keyring) (string, string, error) {
	if !t.Present() {
		return "", "", nil
	}

	var bundled string
	err := sealed.OpenJSON(t.Material, t.KeyID, keys, &bundled)

	if errors.Is(err, sealed.ErrKeyMissing) {
		return "", "", fault.Conflict("seal_key_missing",
			"this forward's certificate was sealed with key "+t.KeyID+
				", which this control plane does not hold")
	}
	if err != nil {
		return "", "", fault.Internal(fmt.Errorf("unseal the certificate: %w", err))
	}

	certPEM, keyPEM := certs.Split(bundled)
	if certPEM == "" || keyPEM == "" {
		return "", "", fault.Internal(errors.New(
			"the sealed bundle holds no certificate or no key"))
	}
	return certPEM, keyPEM, nil
}

func (s *service) certificateOf(f Forward) (string, string, error) {
	return openCertificate(f.TLS, s.sealing)
}

func (s *service) setCertificate(
	ctx context.Context, id, projectID, certPEM, keyPEM string,
) (Forward, error) {
	f, err := s.getIn(ctx, id, projectID)
	if err != nil {
		return Forward{}, err
	}

	var carried TLS
	if certPEM != "" || keyPEM != "" {
		if carried, err = sealCertificate(certPEM, keyPEM, s.sealing); err != nil {
			return Forward{}, err
		}
	}

	if err := s.repo.setTLS(ctx, f.ID, carried); err != nil {
		return Forward{}, translate(err)
	}
	return s.getIn(ctx, id, projectID)
}
