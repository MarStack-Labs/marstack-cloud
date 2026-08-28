package events

import "context"

type Severity string

const (
	Info  Severity = "info"
	Warn  Severity = "warn"
	Error Severity = "error"
)

type Entry struct {
	ProjectID string
	Kind      string
	Subject   string
	NodeID    string
	Message   string
	Severity  Severity
}

type Recorder interface {
	Record(ctx context.Context, entry Entry)
}
