package inspection

import (
	"context"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
)

// InspectOutput returns lifecycle metadata as recorded through this view.
func (v *View) InspectOutput(ctx context.Context, id identity.OutputID) (projection.OutputView, error) {
	ctx, done, err := v.reader.begin(ctx)
	if err != nil {
		return projection.OutputView{}, err
	}
	defer done()
	if _, err = v.log.Head(ctx); err != nil {
		return projection.OutputView{}, err
	}
	return v.projection.Output(id)
}
