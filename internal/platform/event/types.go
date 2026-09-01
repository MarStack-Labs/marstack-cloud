package event

import "time"

const (
	DefaultLimit = 100
	MaxLimit     = 500
	Retain       = 20000
	PruneEvery   = 500

	MaxMessage = 500
	MaxKind    = 60
)

type Entry struct {
	ID        int64
	At        time.Time
	ProjectID string
	Kind      string
	Subject   string
	NodeID    string
	Message   string
	Severity  string
}

type Filter struct {
	ProjectID string
	Subject   string
	Kind      string
	Severity  string
	Limit     int
	Before    int64
}
