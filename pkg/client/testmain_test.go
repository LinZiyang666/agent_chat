package client

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/LinZiyang666/agentchat/internal/crypto"
)

// TestMain lowers bcrypt.APITokenCost for the pkg/client test binary,
// mirroring internal/api/testmain_test.go. See that file for the
// motivation (M8-Q-P1-008 / M8-T-P1-005).
func TestMain(m *testing.M) {
	crypto.APITokenCost = bcrypt.MinCost
	os.Exit(m.Run())
}
