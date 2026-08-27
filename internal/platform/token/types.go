package token

import "time"

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
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
}

type Identity struct {
	ID        string
	Name      string
	Role      string
	ProjectID string
}

type CreateParams struct {
	Name      string
	Role      string
	ProjectID string
}

func Roles() []string {
	return []string{RoleAdmin, RoleMember, RoleNode}
}
