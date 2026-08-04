package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jssblck/akari/internal/server/store"
)

// writeJSON encodes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreErr maps a store read's error onto the response and reports whether it
// wrote one, so a handler reads `if writeStoreErr(...) { return }` instead of
// respelling the ErrNotFound/500 pair. Keeping the mapping in one place is what makes
// a new sentinel (or a 409/403 arm) a single edit rather than a sweep over every
// handler, and it stops the same lookup from 404ing on one route and 500ing on
// another. missing and failure are the two messages the caller would have written.
func writeStoreErr(w http.ResponseWriter, err error, missing, failure string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, missing)
	default:
		writeError(w, http.StatusInternalServerError, failure)
	}
	return true
}

// decodeJSON reads a JSON request body into v, rejecting bodies over a small
// limit and unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	// Reject trailing data after the first JSON value.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain a single JSON object")
		return false
	}
	return true
}
