package watch

import "github.com/jssblck/akari/internal/client/discover"

// markDiscoveredChanges refreshes the known-file baseline and queues only files
// that are new or whose observable metadata changed since the previous pass.
func markDiscoveredChanges(known map[discover.File]fileMeta, files []discover.File, mark func(discover.File)) {
	for _, f := range files {
		current, ok := statMeta(f.Path)
		if !ok {
			continue
		}
		previous, tracked := known[f]
		known[f] = current
		if !tracked || current != previous {
			mark(f)
		}
	}
}
