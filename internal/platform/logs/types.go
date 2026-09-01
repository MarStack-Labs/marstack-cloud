package logs

import "time"

const (
	MaxLineBytes        = 2048
	MaxLinesPerInstance = 2000
	MaxLinesPerReport   = 500
	MaxTail             = 1000
	DefaultTail         = 100
)

type Line struct {
	Seq        int64
	InstanceID string
	Text       string
	ReceivedAt time.Time
}

type ReportParams struct {
	NodeID     string
	InstanceID string
	Texts      []string
}
