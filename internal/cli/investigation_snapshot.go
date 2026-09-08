package cli

import (
	"context"
	"fmt"

	"github.com/guijoazeiro/prodmap/internal/investigation"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
)

// composeInvestigationSnapshot evaluates every read model through one deferred
// read transaction, then releases it before any rendering or file output.
func composeInvestigationSnapshot(ctx context.Context, store *prodmapsqlite.Store, query investigation.Query) (result investigation.Result, err error) {
	snapshot, err := store.BeginReadSnapshot(ctx)
	if err != nil {
		return investigation.Result{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = snapshot.Close()
		}
	}()

	result, err = investigation.Compose(ctx, snapshot, query)
	if err != nil {
		return investigation.Result{}, err
	}
	if err := snapshot.Close(); err != nil {
		return investigation.Result{}, fmt.Errorf("close investigation read snapshot: %w", err)
	}
	closed = true
	return result, nil
}
