package token

import "time"

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
	RoleNode   = "node"

	BootstrapName     = "bootstrap"
	BootstrapFileName = "bootstrap-token"

	defaultProjectID = "prj-default"
)

type Token struct {
	ID         string
	Name       string
	Role       string
	ProjectID  string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

func (t Token) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)
}

type Identity struct {
	ID        string
	Name      string
	Role      string
	ProjectID string
	ExpiresAt time.Time
}

type CreateParams struct {
	Name      string
	Role      string
	ProjectID string
	ExpiresIn string
}

func Roles() []string {
	return []string{RoleAdmin, RoleMember, RoleViewer, RoleNode}
}
