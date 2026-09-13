package inspection

import (
	"context"

	"github.com/stevemurr/strap/identity"
)

// InspectOutput returns lifecycle metadata as recorded through this view.
func (v *View) InspectOutput(ctx context.Context, id identity.OutputID) (OutputView, error) {
	ctx, done, err := v.reader.begin(ctx)
	if err != nil {
		return OutputView{}, err
	}
	defer done()
	if _, err = v.log.Head(ctx); err != nil {
		return OutputView{}, err
	}
	return v.output(id)
}
