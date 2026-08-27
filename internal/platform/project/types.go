package project

import "time"

const (
	DefaultID   = "prj-default"
	DefaultName = "default"
)

type Project struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

type CreateParams struct {
	Name string
}
