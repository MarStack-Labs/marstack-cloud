package webhook

import "time"

const (
	StatePending   = "pending"
	StateDelivered = "delivered"
	StateFailed    = "failed"

	MaxKinds        = 32
	MaxKindLength   = 60
	MaxURLLength    = 500
	MaxAttempts     = 6
	MaxBatch        = 100
	MaxDueBatch     = 20
	FirstBackoff    = 10 * time.Second
	MaxBackoff      = 30 * time.Minute
	DeliverTimeout  = 10 * time.Second
	FanOutEvery     = 5 * time.Second
	RetainFailed    = 500
	SignatureHeader = "Marstack-Signature"
	EventHeader     = "Marstack-Event"
)

type Subscription struct {
	ID        string
	ProjectID string
	Name      string
	URL       string
	Secret    string
	Kinds     []string
	Active    bool
	CreatedAt time.Time
}

type CreateParams struct {
	ProjectID string
	Name      string
	URL       string
	Kinds     []string
}

type Delivery struct {
	ID             string
	SubscriptionID string
	EventID        int64
	Kind           string
	Subject        string
	State          string
	Attempts       int
	LastError      string
	NextAttemptAt  time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Event struct {
	ID        int64
	At        time.Time
	ProjectID string
	Kind      string
	Subject   string
	NodeID    string
	Message   string
	Severity  string
}

func States() []string {
	return []string{StatePending, StateDelivered, StateFailed}
}
