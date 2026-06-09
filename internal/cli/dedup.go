package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WO-32: a Postfix pipe transport can RE-DELIVER the same trigger to the filter
// (a slow-but-succeeding pipe gets re-queued), producing duplicate receipts. This
// is not a loop — the Auto-Submitted guard sees a clean trigger each run — so it
// needs its own defense: an idempotency store keyed on the trigger's Message-ID.
//
// The claim is atomic via os.Mkdir (POSIX-atomic): two simultaneous re-deliveries
// race, exactly one wins, the loser is suppressed. Opt-in via --dedup-dir; default
// off preserves existing behavior.

// dedupTTL bounds how long a trigger is remembered. Re-deliveries happen within
// seconds; a day is generous and an external cron can prune older entries.
const dedupTTL = 24 * time.Hour

// triggerMessageID extracts the trigger email's Message-ID (the re-delivered
// envelope's own id, which Postfix preserves across re-delivery — NOT the attached
// sent message's id). Returns "" when absent.
func triggerMessageID(raw []byte) string {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(msg.Header.Get("Message-ID"))
}

// claimTrigger records this trigger as processed and reports whether the caller
// should proceed. It returns true exactly once per trigger Message-ID within the
// TTL; a repeat returns false (suppress the duplicate). An empty dir disables
// dedup (always proceeds). An empty/absent Message-ID cannot be deduplicated
// safely, so it always proceeds (fail open: never silently drop a real receipt
// just because the trigger lacked an id).
func claimTrigger(dir string, raw []byte) bool {
	if dir == "" {
		return true
	}
	mid := triggerMessageID(raw)
	if mid == "" {
		return true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true // cannot persist state -> do not suppress a real receipt
	}
	sum := sha256.Sum256([]byte(mid))
	lock := filepath.Join(dir, hex.EncodeToString(sum[:]))

	// If a fresh claim exists within the TTL, this is a duplicate -> suppress.
	if fi, err := os.Stat(lock); err == nil {
		if time.Since(fi.ModTime()) < dedupTTL {
			return false
		}
		// Stale claim: a much later resend of the same id. Refresh and proceed.
		_ = os.Remove(lock)
	}
	// Atomic claim: Mkdir succeeds for exactly one racer.
	if err := os.Mkdir(lock, 0o700); err != nil {
		// Lost the race (another concurrent delivery claimed it) -> duplicate.
		return false
	}
	return true
}
