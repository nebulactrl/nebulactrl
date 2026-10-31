//go:build integration

package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// envTier selects the test tier (see tests/README.md):
//
//	smoke  the core scenarios only: what `make test-smoke` and CI's tier 2 run
//	full   everything (default when run by hand): `make test-full`, nightly CI
const envTier = "NEBULACTRL_TEST_TIER"

type tier int

const (
	tierSmoke tier = iota + 1
	tierFull
)

func (t tier) String() string {
	if t == tierSmoke {
		return "smoke"
	}
	return "full"
}

// activeTier is the tier of this run, set by TestMain.
var activeTier = tierFull

// parseTier reads NEBULACTRL_TEST_TIER; unset means full.
func parseTier() (tier, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(envTier))); v {
	case "", "full":
		return tierFull, nil
	case "smoke":
		return tierSmoke, nil
	default:
		return 0, fmt.Errorf("%s=%q: want smoke or full", envTier, v)
	}
}

// requireTier declares the lowest tier a test belongs to and skips it when the
// run's tier is lower: requireTier(t, tierSmoke) tests run in both tiers,
// requireTier(t, tierFull) tests only in the full one. Every test calls it
// first, so `grep requireTier` is the tier list.
func requireTier(t testing.TB, want tier) {
	t.Helper()
	if want > activeTier {
		t.Skipf("%s-tier test; this run is %s (%s)", want, activeTier, envTier)
	}
}
