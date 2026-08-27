package keypair

import "time"

const (
	MaxKeyBytes = 8 * 1024
	MaxPerName  = 64
)

type Key struct {
	ID          string
	ProjectID   string
	Name        string
	PublicKey   string
	Fingerprint string
	Kind        string
	Comment     string
	CreatedAt   time.Time
}

type CreateParams struct {
	ProjectID string
	Name      string
	PublicKey string
}
