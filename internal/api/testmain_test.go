package api

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/LinZiyang666/agentchat/internal/crypto"
)

// TestMain lowers bcrypt.APITokenCost from the production 12 to
// bcrypt.MinCost (4) before any test runs. Production cost 12 is
// ~150ms/hash on commodity hardware × 30+ tokens issued across this
// package's tests × the race-detector's ~5x overhead = the single
// biggest contributor to the ~17 min `go test -race ./internal/api`
// wall-clock. MinCost cuts that to ~1ms/hash for a ~256x speedup of
// the hashing path; without changing any test or production behavior.
//
// Fix for M8-Q-P1-008 / M8-T-P1-004.
func TestMain(m *testing.M) {
	crypto.APITokenCost = bcrypt.MinCost
	os.Exit(m.Run())
}
