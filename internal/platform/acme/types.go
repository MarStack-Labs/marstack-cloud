package acme

import "time"

const (
	StatePending = "pending"
	StateIssued  = "issued"
	StateFailed  = "failed"

	MaxNames     = 8
	MaxNameChars = 253

	SweepInterval  = 30 * time.Second
	ChallengeGrace = 30 * time.Second
	RenewBefore    = 30 * 24 * time.Hour
	OrderLimit     = 10 * time.Minute
)

type Order struct {
	ID         string
	ProjectID  string
	BalancerID string
	Names      []string
	State      string
	Message    string
	OrderURL   string
	KeyPEM     string
	Challenges []Challenge
	IssuedAt   time.Time
	ExpiresAt  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Challenge struct {
	Name          string
	Token         string
	Authorization string
	URL           string
	Accepted      bool
}

type StartParams struct {
	ProjectID  string
	BalancerID string
	Names      []string
	Contact    string
}
