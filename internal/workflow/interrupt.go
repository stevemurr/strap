package workflow

import (
	"context"

	"github.com/stevemurr/strap/work"
)

// Suspend prevents new mutations and dispatcher work. The harness joins already
// admitted operations and agents before settling the ledger and pending notices.
func (s *Session) Suspend()           { s.interrupted.Store(true) }
func (s *Session) ResumeInterrupted() { s.interrupted.Store(false) }

func (s *Session) SettleInterrupt(ctx context.Context) error {
	result := make(chan error, 1)
	select {
	case s.interruptDrain <- result:
	case <-s.done:
		return s.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-result:
		return err
	case <-s.done:
		return s.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) cancelInterruptedWork() error {
	root := s.Root()
	for _, item := range s.Store.ActorState(root).Owned {
		// Cancel may also close children and revise the parent. Fetch each current
		// revision rather than reusing the enumeration's pre-cancellation version.
		w, err := s.Store.GetWork(root, item.WorkID)
		if err != nil {
			return err
		}
		if w.State.Terminal() {
			continue
		}
		if _, err := s.Store.Cancel(root, work.CancelRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Reason: "User interrupted execution"}); err != nil {
			return err
		}
	}
	for _, event := range s.Store.PendingEvents(0) {
		if err := s.Store.AcknowledgeEvent(event.ID); err != nil {
			return err
		}
	}
	return nil
}
