package harness

import (
	"context"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

func (s *Session) ListWork(ctx context.Context, actor identity.ActorID, q work.ListQuery) (work.ListPage, error) {
	if actor == "" || actor != s.Root() {
		return work.ListPage{}, work.ErrForbidden
	}
	reader, err := s.Trace(ctx)
	if err != nil {
		return work.ListPage{}, err
	}
	defer reader.Close(context.Background())
	return reader.ListWork(ctx, actor, q)
}
