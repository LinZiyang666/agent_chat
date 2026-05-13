// Package auth handles API token issuance, verification (bcrypt), and
// revocation for CLI-to-daemon authentication. Tokens are scoped per
// account; multiple tokens per account are permitted. See
// docs/03-architecture.md D6.
package auth
