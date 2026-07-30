package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Fingerprint identifies "the same fact said again". Scope ids are hashed as
// stored, without case folding: two users whose ids differ only in case are two
// users, and folding them here would let one user's Save return the other's
// item through the deduplication index.
func Fingerprint(scope Scope, kind Kind, content string) string {
	canonical := strings.Join([]string{
		scope.WorkspaceID,
		scope.UserID,
		string(scope.Visibility),
		string(kind),
		// Content is folded, because "Reset value is 0x10" and "reset value is
		// 0x10" are the same fact. \x00 separators keep fields unambiguous, so
		// no shifting of characters between them can forge a collision.
		strings.ToLower(normalizeText(content)),
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}
