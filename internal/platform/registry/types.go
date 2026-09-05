package registry

import "time"

const (
	MaxHostLength     = 253
	MaxUsernameLength = 255
	MaxPasswordLength = 4096
)

type Credential struct {
	ID        string
	Host      string
	Username  string
	Sealed    string
	KeyID     string
	CreatedAt time.Time
}

type CreateParams struct {
	Host     string
	Username string
	Password string
}

type Open struct {
	Host     string
	Username string
	Password string
}
