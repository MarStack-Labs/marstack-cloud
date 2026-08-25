package instance

import "time"

type Isolation string

const (
	IsolationContainer Isolation = "container"
	IsolationVM        Isolation = "vm"
	IsolationMicroVM   Isolation = "microvm"
)

func AllIsolations() []string {
	return []string{
		string(IsolationContainer),
		string(IsolationVM),
		string(IsolationMicroVM),
	}
}

type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

type ObservedState string

const (
	ObservedPending ObservedState = "pending"
	ObservedRunning ObservedState = "running"
	ObservedStopped ObservedState = "stopped"
	ObservedFailed  ObservedState = "failed"
)

func AllObservedStates() []string {
	return []string{
		string(ObservedPending),
		string(ObservedRunning),
		string(ObservedStopped),
		string(ObservedFailed),
	}
}

const MaxObservedMessage = 512

const (
	MinVCPU      = 1
	MaxVCPU      = 256
	MinMemoryMiB = 128
	MaxMemoryMiB = 1024 * 1024

	DefaultVCPU      = 1
	DefaultMemoryMiB = 512
)

type Instance struct {
	ID              string
	Name            string
	Isolation       Isolation
	Image           string
	VCPU            int
	MemoryMiB       int
	Desired         DesiredState
	Observed        ObservedState
	ObservedMessage string
	NodeID          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateParams struct {
	Name      string
	Isolation string
	Image     string
	VCPU      int
	MemoryMiB int
}
