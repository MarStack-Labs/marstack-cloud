package scope

import "context"

type Scope struct {
	ProjectID string
	TokenID   string
	TokenName string
	Role      string
}

type key struct{}

func With(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, key{}, s)
}

func From(ctx context.Context) Scope {
	s, _ := ctx.Value(key{}).(Scope)
	return s
}
