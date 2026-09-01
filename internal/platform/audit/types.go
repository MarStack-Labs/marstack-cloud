package audit

import "time"

const (
	DefaultLimit = 100
	MaxLimit     = 500
	Retain       = 5000
)

type Entry struct {
	ID        int64
	At        time.Time
	Actor     string
	UserID    string
	Role      string
	ProjectID string
	Method    string
	Path      string
	Status    int
	RequestID string
}

type Record struct {
	Actor     string
	UserID    string
	Role      string
	ProjectID string
	Method    string
	Path      string
	Status    int
	RequestID string
}
