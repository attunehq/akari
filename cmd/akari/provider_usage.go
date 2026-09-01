package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jssblck/akari/internal/client/provider"
	"github.com/jssblck/akari/internal/client/upload"
	"github.com/jssblck/akari/internal/config"
)

// collectProviderUsage runs the vendor usage collections and reports them on
// stderr, returning the joined error of any that failed.
//
// A skip is printed only when it says something the operator can act on, and never
// as an error: a machine with no Cursor install has nothing to collect, and saying
// so on every sync would be noise on the overwhelming majority of machines.
func collectProviderUsage(ctx context.Context, cfg config.Client, client *upload.Client, home string) error {
	var errs []error
	for _, r := range provider.Collect(ctx, cfg, client, home, os.Getenv) {
		switch {
		case r.Err != nil:
			errs = append(errs, fmt.Errorf("collect %s usage: %w", r.Provider, r.Err))
		case r.Skipped:
			// Nothing to do on this machine. Kept off stdout so a scripted sync's
			// summary stays about the transcripts it moved.
			continue
		case r.Inserted > 0:
			fmt.Fprintf(os.Stderr, "%s usage: %d new event(s) of %d fetched\n", r.Provider, r.Inserted, r.Fetched)
		}
	}
	return errors.Join(errs...)
}
