package user

import "time"

const (
	MaxNameLength     = 60
	MaxEmailLength    = 254
	MinPasswordLength = 12
	MaxPasswordLength = 256

	SessionLifetime = 12 * time.Hour

	LoginsPerSecond = 1
	LoginBurst      = 5
	MaxLoginKeys    = 4096
)

type User struct {
	ID        string
	Email     string
	Name      string
	Role      string
	ProjectID string
	Disabled  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateParams struct {
	Email     string
	Name      string
	Role      string
	ProjectID string
	Password  string
}

type Session struct {
	Secret    string
	UserID    string
	Email     string
	Role      string
	ProjectID string
	ExpiresAt time.Time
}
