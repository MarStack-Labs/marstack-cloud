package balancer

import (
	"context"
	"testing"
)

type fakeServices struct {
	members map[string][]string
}

func (f fakeServices) MembersOf(_ context.Context, _, name string) ([]string, error) {
	held, known := f.members[name]
	if !known {
		return nil, errNotFound
	}
	return held, nil
}

func TestDeletingABalancerTakesItsRoutesWithIt(t *testing.T) {
	m, _ := newTestModule(t)
	m.UseServices(fakeServices{members: map[string][]string{"web": {"i-1"}}})

	ctx := context.Background()
	created, err := m.svc.create(ctx, CreateParams{
		ProjectID:  "prj-default",
		Name:       "edge",
		TargetPort: 80,
		ListenPort: 8443,
		Routes:     []RouteParams{{Host: "app.test", Service: "web"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := m.svc.remove(ctx, "prj-default", created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	left, err := m.svc.repo.routesOf(ctx, created.ID)
	if err != nil {
		t.Fatalf("read the routes back: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("routes = %+v, want none: a route table nothing owns is a row per route "+
			"that nothing will ever clean up, and no API can see it to complain", left)
	}
}
