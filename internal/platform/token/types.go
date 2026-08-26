package token

import "time"

const (
	RoleAdmin = "admin"
	RoleNode  = "node"

	BootstrapName     = "bootstrap"
	BootstrapFileName = "bootstrap-token"
)

type Token struct {
	ID         string
	Name       string
	Role       string
	CreatedAt  time.Time
	LastUsedAt time.Time
}

type Identity struct {
	ID   string
	Name string
	Role string
}

type CreateParams struct {
	Name string
	Role string
}

func Roles() []string {
	return []string{RoleAdmin, RoleNode}
}
