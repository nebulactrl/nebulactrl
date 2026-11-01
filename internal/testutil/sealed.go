package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/secretbox/rekey"
)

// RequireSealedValuesOpen fails the test when any sealed value in the database
// does not open with key under the context the registry (secretbox/registry.go)
// derives from its row. Production code seals under a row id it computes in Go;
// this walks every registered column with the SQL row key expression and opens
// every value, so a call site and the registry that disagree show up here
// instead of in `rekey` on a real instance. Nothing is changed: values already
// on the key are only verified.
func RequireSealedValuesOpen(t testing.TB, pool *pgxpool.Pool, key []byte) {
	t.Helper()
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("sealed values: %v", err)
	}
	rep, err := rekey.Run(context.Background(), pool, box, rekey.Options{Verify: box})
	if err != nil {
		t.Fatalf("sealed values: %v", err)
	}
	if rep.Failed() > 0 {
		for _, c := range rep.Columns {
			for _, id := range c.FailedRows {
				t.Errorf("sealed value %s of row %s does not open under its registry context", c.Column, id)
			}
		}
		t.FailNow()
	}
}
