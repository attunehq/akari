package store

import "errors"

// ErrNotFound is returned by lookups that match no row.
var ErrNotFound = errors.New("not found")

// ErrNotOrphaned is returned when AssignSessionProject is asked to move a
// session that is neither in an orphaned project nor already pinned. A remote
// or standalone session got its project from live resolution; a pin is the
// override for worktrees that can no longer resolve.
var ErrNotOrphaned = errors.New("session is not orphaned")

// ErrInvalidInvite is returned when registration presents an invite token that
// is unknown, already redeemed, or expired.
var ErrInvalidInvite = errors.New("invalid or already used invite token")
