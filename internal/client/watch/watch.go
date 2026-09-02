// Package watch keeps session files synced continuously. fsnotify drives prompt,
// debounced uploads of changed files; a periodic poll catches roots the OS
// watcher cannot cover (network filesystems, watch exhaustion); and a slow full
// rescan is the safety net for anything both missed.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jssblck/akari/internal/client/discover"
	"github.com/jssblck/akari/internal/client/syncer"
)

var errWatcherClosed = errors.New("filesystem watcher stopped unexpectedly")

// SyncFunc syncs one file. watch depends on this rather than the concrete syncer
// so it can be tested without a server.
type SyncFunc func(ctx context.Context, f discover.File) syncer.Result

// Options tune the watch timers. Zero values fall back to defaults.
type Options struct {
	Debounce        time.Duration // quiet period before uploading a changed file
	Poll            time.Duration // mtime/size re-stat interval for the polling fallback
	Discover        time.Duration // interval to re-walk the roots for newly created files
	Rescan          time.Duration // rediscovery safety net interval
	Usage           time.Duration // vendor account-usage collection interval
	PressureBackoff time.Duration // pause after a network, server, or process-capacity failure
	// StoreRefresh sets the hard deadline for a coalesced OpenCode/omp store
	// discovery while the store is being written continuously. See storeRefresh.
	StoreRefresh time.Duration
	// Excludes are glob patterns of paths to skip (see discover.Excluder). They
	// keep an ignored location out of discovery, the poll, and event handling.
	Excludes []string
	// CollectUsage collects vendor-reported account usage (see client/provider). It
	// runs on its own slow ticker rather than alongside a file sync because what it
	// fetches is account-wide: no file changing on this machine makes it due, and no
	// machine having the file makes it undue. Nil disables the ticker entirely, which
	// is what the watcher's own tests use.
	CollectUsage func(context.Context)
	Logf         func(string, ...any)
}

func (o Options) withDefaults() Options {
	if o.Debounce <= 0 {
		o.Debounce = 500 * time.Millisecond
	}
	if o.Poll <= 0 {
		o.Poll = 3 * time.Second
	}
	if o.Discover <= 0 {
		o.Discover = 30 * time.Second
	}
	if o.Rescan <= 0 {
		o.Rescan = 15 * time.Minute
	}
	// Vendor usage is billing data, settled on the vendor's side minutes after the
	// fact and read by nobody in real time, so it collects on a far slower cadence
	// than anything file-driven. Each pass is a handful of requests against one
	// account, so a long interval costs a user nothing but keeps akari from polling
	// a third party every few seconds.
	if o.Usage <= 0 {
		o.Usage = 30 * time.Minute
	}
	if o.PressureBackoff <= 0 {
		o.PressureBackoff = 30 * time.Second
	}
	// A store event coalesces rather than discovering inline, so this sets the
	// deadline for picking up a session in an actively-written store. The pass
	// runs on the first flush tick at or after it; 5s is well under the 30s
	// discover safety net and above the pathological per-transaction event rate.
	if o.StoreRefresh <= 0 {
		o.StoreRefresh = 5 * time.Second
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	return o
}

// Watcher watches a set of roots and syncs changed session files.
type Watcher struct {
	roots []discover.Root
	sync  SyncFunc
	opt   Options
	ex    discover.Excluder

	// discoveryLog dedupes the log line w.discover() emits; see logDiscoveryStatus.
	// It is only ever touched from run()'s single goroutine.
	discoveryLog discoveryLogState
}

// New builds a Watcher.
func New(roots []discover.Root, sync SyncFunc, opt Options) *Watcher {
	o := opt.withDefaults()
	return &Watcher{roots: roots, sync: sync, opt: o, ex: discover.NewExcluder(o.Excludes)}
}

type fileMeta struct {
	mod  time.Time
	size int64
}

// Run watches until ctx is cancelled, then returns ctx.Err(). It performs an
// initial full sync pass before entering the event loop.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	return w.run(ctx, fsw)
}

func (w *Watcher) run(ctx context.Context, fsw *fsnotify.Watcher) error {
	defer fsw.Close()

	for _, r := range w.roots {
		if err := w.addRecursive(fsw, r); err != nil {
			w.opt.Logf("watch root %s: %v", r.Dir, err)
		}
	}

	rs := &runState{
		w:     w,
		dirty: map[discover.File]struct{}{},
		wake:  make(chan struct{}, 1),
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		rs.worker(workerCtx)
		close(done)
	}()
	defer func() {
		cancelWorker()
		<-done
	}()

	// Initial pass: discover everything, seed the poll baseline, and sync all. The
	// baseline is keyed by File so the poll can re-stat the known set directly and
	// has the File in hand to queue a changed one, without re-walking the tree.
	known := map[discover.File]fileMeta{}
	for _, f := range w.discover() {
		if m, ok := statMeta(f.Path); ok {
			known[f] = m
		}
		rs.mark(f)
	}

	pending := map[discover.File]time.Time{}
	sr := &storeRefresh{}
	flush := time.NewTicker(flushInterval(w.opt.Debounce))
	poll := time.NewTicker(w.opt.Poll)
	disco := time.NewTicker(w.opt.Discover)
	rescan := time.NewTicker(w.opt.Rescan)
	defer flush.Stop()
	defer poll.Stop()
	defer disco.Stop()
	defer rescan.Stop()

	// A nil collector leaves the channel nil, which blocks forever in the select
	// below, so the loop keeps exactly the shape it had without the collection.
	var usageC <-chan time.Time
	// collect runs one usage pass off the run loop. A first collection walks the
	// account's whole history, which is many requests: running it inline would stall
	// this goroutine for its full duration, and this goroutine is what drains the
	// fsnotify channel (whose kernel queue can overflow and drop events), fires the
	// debounce deadlines, and services the poll and rescan tickers. File syncs already
	// run off the loop for the same reason. The guard makes passes single-flight, so a
	// tick that lands while a slow pass is still running is dropped rather than
	// stacking a second walk of the same feed on top of it.
	//
	// An in-flight pass is cancelled and then joined before run returns. run also
	// returns on errWatcherClosed with ctx still live, so without its own cancellable
	// context a collection would keep issuing requests after the watcher reported
	// shutdown and then be killed mid-request by process exit. The defers are ordered
	// so the cancel runs first and the wait cannot block on a pass that is still
	// walking the feed. Abandoning a pass is free: it is idempotent and resumes from
	// the server's watermark.
	collectCtx, cancelCollect := context.WithCancel(ctx)
	var collecting atomic.Bool
	var collectors sync.WaitGroup
	defer collectors.Wait()
	defer cancelCollect()
	collect := func() {
		if w.opt.CollectUsage == nil || !collecting.CompareAndSwap(false, true) {
			return
		}
		collectors.Add(1)
		go func() {
			defer collectors.Done()
			defer collecting.Store(false)
			w.opt.CollectUsage(collectCtx)
		}()
	}
	if w.opt.CollectUsage != nil {
		usage := time.NewTicker(w.opt.Usage)
		defer usage.Stop()
		usageC = usage.C
		// Collect once at startup rather than waiting out a full interval, so a
		// freshly started daemon reports the account's usage now instead of in half an
		// hour.
		collect()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-fsw.Events:
			if !ok {
				return errWatcherClosed
			}
			w.handleEvent(fsw, ev, known, pending, sr)

		case err, ok := <-fsw.Errors:
			if !ok {
				return errWatcherClosed
			}
			if err != nil {
				w.opt.Logf("watch error: %v", err)
			}

		case now := <-flush.C:
			flushTick(now, w.opt.Debounce, sr, known, pending, w.discover, rs.mark)

		case <-usageC:
			collect()

		case <-poll.C:
			// Fallback for changes the OS watcher missed: re-stat only the files we
			// already know about (no tree walk) and queue the changed ones. Finding
			// newly created files is the discover ticker's job below, so the frequent
			// poll stays O(known files) of stat syscalls rather than re-walking and
			// re-sorting the whole session tree every few seconds.
			for f, prev := range known {
				m, ok := statMeta(f.Path)
				if !ok {
					delete(known, f) // gone from disk; stop tracking it
					continue
				}
				if m != prev {
					known[f] = m
					pending[f] = time.Now().Add(w.opt.Debounce)
				}
			}

		case <-disco.C:
			// Catch files created on a root the OS watcher cannot cover (a network
			// filesystem, or one past the watch limit), where no Create event fires.
			// This walks the tree, but on a slower cadence than the poll, so a brand
			// new file there appears within this interval rather than every poll
			// paying for a walk. A file fsnotify did see is already syncing via its
			// Create event; this only adds the ones missing from the baseline.
			for _, f := range w.discover() {
				if _, ok := known[f]; ok {
					continue
				}
				if m, ok := statMeta(f.Path); ok {
					known[f] = m
				}
				rs.mark(f)
			}

		case <-rescan.C:
			// Safety net: re-add directories and catch files whose metadata changed
			// without an event. Re-syncing the full corpus here would re-read and
			// transform every historical session even while the machine is idle.
			for _, r := range w.roots {
				if err := w.addRecursive(fsw, r); err != nil {
					w.opt.Logf("watch root %s: %v", r.Dir, err)
				}
			}
			markDiscoveredChanges(known, w.discover(), rs.mark)
		}
	}
}

// runState holds the dirty set shared between the event loop (producer) and the
// worker (consumer). The set is unbounded but deduplicated by file, so no change
// is ever dropped and a busy file is collapsed to a single pending sync.
type runState struct {
	w     *Watcher
	mu    sync.Mutex
	dirty map[discover.File]struct{}
	wake  chan struct{}
}

// mark records a file as needing a sync and nudges the worker.
func (r *runState) mark(f discover.File) {
	r.mu.Lock()
	r.dirty[f] = struct{}{}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default: // a wake is already pending; the worker will drain everything
	}
}

// pop removes and returns one dirty file unless shutdown began while the worker
// was waiting for the dirty-set lock.
func (r *runState) pop(ctx context.Context) (discover.File, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return discover.File{}, false
	}
	for f := range r.dirty {
		delete(r.dirty, f)
		return f, true
	}
	return discover.File{}, false
}

// worker drains the dirty set one file at a time. Uploads are idempotent, so a
// file re-marked while in flight simply syncs again on the next drain.
//
// Syncs run on a context detached from ctx so the file the worker is on finishes
// uploading after a Ctrl-C; once that file is done the worker stops instead of
// draining the rest of the backlog. A second Ctrl-C exits the process outright
// (handled by the signal layer), so a slow upload never blocks shutdown forever.
func (r *runState) worker(ctx context.Context) {
	work := context.WithoutCancel(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		for {
			f, ok := r.pop(ctx)
			if !ok {
				break
			}
			if ctx.Err() != nil {
				return
			}
			res := r.w.sync(work, f)
			switch {
			case res.Skipped:
				r.w.opt.Logf("skip %s: %s", f.Path, res.Reason)
			case res.Err != nil:
				r.w.opt.Logf("error %s: %v", f.Path, res.Err)
			case res.UploadedBytes > 0:
				r.w.opt.Logf("uploaded %s -> %s (%d bytes)", f.Path, res.Destination(), res.UploadedBytes)
			}
			if pressureFailure(res.Err) {
				r.mark(f)
				r.w.opt.Logf("watch paused for %s after resource pressure", r.w.opt.PressureBackoff)
				if !waitForPressureBackoff(ctx, r.w.opt.PressureBackoff) {
					return
				}
			}
			if ctx.Err() != nil {
				return // finished the current file; stop without draining the backlog
			}
		}
	}
}

// storeRefresh coalesces OpenCode/omp store events into a single discovery pass.
//
// One SQLite transaction touches the database, its -wal and its -shm, and an
// active agent commits many times a second, so every such write used to run a
// full discovery inline on the run loop: open the database, probe its schema,
// list every session and lstat every materialized transcript. Measured at ~300ms
// a pass against a 1.2GB store, that burned an entire core for as long as an
// agent was running, and starved the same loop that drains fsnotify's queue.
//
// Events now schedule a pass instead of performing one. settleAt runs it once
// writes go quiet, which is the common case; forceAt bounds the wait so a store
// under continuous write still gets picked up rather than being starved by a
// deadline that keeps moving.
type storeRefresh struct {
	pending  bool
	settleAt time.Time
	forceAt  time.Time
}

func (s *storeRefresh) schedule(now time.Time, settle, maxWait time.Duration) {
	if !s.pending {
		s.pending = true
		s.forceAt = now.Add(maxWait)
	}
	s.settleAt = now.Add(settle)
}

func (s *storeRefresh) due(now time.Time) bool {
	return s.pending && (!now.Before(s.settleAt) || !now.Before(s.forceAt))
}

// flushTick services both coalesced store discovery and ordinary file
// debounces. StoreRefresh is intentionally sampled by this shared ticker rather
// than owning another timer, so a refresh runs on the first flush tick at or
// after its deadline, normally no more than flushInterval(debounce) later.
// Normal timer and run-loop scheduling delay still applies.
func flushTick(
	now time.Time,
	debounce time.Duration,
	sr *storeRefresh,
	known map[discover.File]fileMeta,
	pending map[discover.File]time.Time,
	discoverFiles func() []discover.File,
	mark func(discover.File),
) {
	if sr.due(now) {
		sr.pending = false
		deadline := now.Add(debounce)
		for _, f := range discoverFiles() {
			pending[f] = deadline
			if m, ok := statMeta(f.Path); ok {
				known[f] = m
			}
		}
	}
	for f, deadline := range pending {
		if !now.Before(deadline) {
			mark(f)
			delete(pending, f)
		}
	}
}

// handleEvent reacts to one filesystem event: new directories are watched
// recursively, and changed session files are scheduled after the debounce. An
// accepted file also enters the poll's known set, so the fast poll covers a Write
// the OS watcher may later miss on that file rather than leaving it uncovered until
// the slower discover ticker folds it in.
func (w *Watcher) handleEvent(fsw *fsnotify.Watcher, ev fsnotify.Event, known map[discover.File]fileMeta, pending map[discover.File]time.Time, sr *storeRefresh) {
	if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
		return
	}
	if w.isOpenCodeStoreEvent(ev.Name) {
		_ = fsw.Add(ev.Name)
		sr.schedule(time.Now(), w.opt.Debounce, w.opt.StoreRefresh)
		return
	}
	if w.withinOpenCodeRoot(ev.Name) {
		return
	}
	if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		if err := w.addRecursive(fsw, discover.Root{Dir: ev.Name}); err != nil {
			w.opt.Logf("watch directory %s: %v", ev.Name, err)
		}
		return
	}
	if f, ok := w.fileFor(ev.Name); ok {
		pending[f] = time.Now().Add(w.opt.Debounce)
		if _, tracked := known[f]; !tracked {
			if m, ok := statMeta(f.Path); ok {
				known[f] = m
			}
		}
	}
}

// fileFor classifies an event path: the root whose directory contains it gives
// the agent, and the agent's session-file match confirms it is a session file.
//
// It resolves each root through discover.ResolveRoot rather than comparing
// against r.Dir directly, because a root accepted via FollowRootLink is watched
// (by addRecursive) and discovered (by discover.Discover) under its resolved
// target directory, not under the link path in the config. Comparing against
// r.Dir there would silently fail to match every live event under a followed
// root, falling back to the slower discover ticker for every one of its files
// instead of the prompt fsnotify path. A root that is unusable or was skipped
// with a notice never matches, the same as it never contributes any files to
// discover.Discover.
func (w *Watcher) fileFor(path string) (discover.File, bool) {
	if w.ex.Excluded(path) {
		return discover.File{}, false
	}
	if _, ok := statMeta(path); !ok {
		return discover.File{}, false
	}
	for _, r := range w.roots {
		dir, notice, err := discover.ResolveRoot(r)
		if err != nil || notice != "" {
			continue
		}
		if within(dir, path) && discover.Matches(r.Agent, path) {
			return discover.File{Agent: r.Agent, Root: dir, Path: path}, true
		}
	}
	return discover.File{}, false
}

func (w *Watcher) discover() []discover.File {
	files, notices, err := discover.Discover(w.roots, w.ex)
	w.logDiscoveryStatus(notices, err)
	return files
}

// discoveryLogState dedupes the log line w.discover() emits. Without it, a
// standing discovery failure (a permanently blocked root, for example) would
// re-log an unchanged line every discover tick (30s by default) forever. It logs
// immediately the first time a notice or error appears and whenever the
// aggregated text changes (a new problem, an old one clearing, or the set of
// affected roots shifting); otherwise it stays quiet except for one reminder
// per discoveryLogReminder, so a standing problem never fully vanishes from the
// log either.
type discoveryLogState struct {
	lastSignature string
	lastLoggedAt  time.Time
}

// discoveryLogReminder bounds how rarely an unchanged, still-broken discovery
// status re-logs once it has already been reported.
const discoveryLogReminder = time.Hour

// logDiscoveryStatus logs discover()'s notices and error, deduped via
// discoveryLogState (see its doc comment for the policy).
func (w *Watcher) logDiscoveryStatus(notices []string, err error) {
	signature := discoveryStatusSignature(notices, err)
	if signature == "" {
		if w.discoveryLog.lastSignature != "" {
			w.opt.Logf("discovery recovered")
		}
		w.discoveryLog = discoveryLogState{}
		return
	}
	now := time.Now()
	changed := signature != w.discoveryLog.lastSignature
	due := !w.discoveryLog.lastLoggedAt.IsZero() && now.Sub(w.discoveryLog.lastLoggedAt) >= discoveryLogReminder
	if !changed && !due {
		return
	}
	for _, n := range notices {
		w.opt.Logf("%s", n)
	}
	if err != nil {
		w.opt.Logf("discovery incomplete (%d error(s)): %v", discover.ErrorCount(err), err)
	}
	w.discoveryLog = discoveryLogState{lastSignature: signature, lastLoggedAt: now}
}

// discoveryStatusSignature reduces one discover() outcome to a single string a
// later call can compare against, so logDiscoveryStatus can tell "still the same
// problem" from "something changed" with a plain string comparison. Empty means
// healthy: no notices and no error.
func discoveryStatusSignature(notices []string, err error) string {
	if err == nil && len(notices) == 0 {
		return ""
	}
	var b strings.Builder
	for _, n := range notices {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	if err != nil {
		b.WriteString(err.Error())
	}
	return b.String()
}

// addRecursive adds root's directory and all of its subdirectories to the
// watcher, skipping any excluded subtree so the watch never spends an fsnotify
// slot on a directory whose files would be filtered out anyway. It applies the
// closed root-link policy through discover.ResolveRoot, the same function
// discover.Discover uses, so a root that discovery would reject or skip is
// rejected or skipped identically here: initial pass, rescan, and a future
// discovery pass can never disagree about whether a given root is usable.
func (w *Watcher) addRecursive(fsw *fsnotify.Watcher, root discover.Root) error {
	if root.Dir == "" {
		return errors.New("path is empty")
	}
	dir, notice, err := discover.ResolveRoot(root)
	if err != nil {
		if root.Optional && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if notice != "" {
		w.opt.Logf("%s", notice)
		return nil
	}
	if root.Agent == "opencode" {
		return w.addOpenCodeStore(fsw, dir, root)
	}
	var problems []error
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != dir && w.ex.ExcludedDir(p) {
			return filepath.SkipDir
		}
		// fsnotify.Add is idempotent. Calling it for every live directory lets a
		// recreated path regain the watch that the OS removed with its predecessor.
		if addErr := fsw.Add(p); addErr != nil {
			problems = append(problems, fmt.Errorf("add %s: %w", p, addErr))
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func (w *Watcher) addOpenCodeStore(fsw *fsnotify.Watcher, dir string, root discover.Root) error {
	if err := fsw.Add(dir); err != nil {
		return fmt.Errorf("add %s: %w", dir, err)
	}
	name := root.SessionDB
	if name == "" {
		name = "opencode.db"
	}
	var problems []error
	for _, base := range []string{name, name + "-wal", name + "-shm"} {
		p := filepath.Join(dir, base)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := fsw.Add(p); err != nil {
			problems = append(problems, fmt.Errorf("add %s: %w", p, err))
		}
	}
	return errors.Join(problems...)
}

func (w *Watcher) isOpenCodeStoreEvent(path string) bool {
	base := filepath.Base(path)
	for _, r := range w.roots {
		if r.Agent != "opencode" {
			continue
		}
		dir, notice, err := discover.ResolveRoot(r)
		if err != nil || notice != "" {
			continue
		}
		if !within(dir, path) {
			continue
		}
		name := r.SessionDB
		if name == "" {
			name = "opencode.db"
		}
		if base == name || base == name+"-wal" || base == name+"-shm" {
			return true
		}
	}
	return false
}

func (w *Watcher) withinOpenCodeRoot(path string) bool {
	for _, r := range w.roots {
		if r.Agent != "opencode" {
			continue
		}
		dir, notice, err := discover.ResolveRoot(r)
		if err != nil || notice != "" {
			continue
		}
		if within(dir, path) {
			return true
		}
	}
	return false
}

func statMeta(path string) (fileMeta, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fileMeta{}, false
	}
	return fileMeta{mod: info.ModTime(), size: info.Size()}, true
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// flushInterval picks how often to check the debounce map: often enough to honor
// the debounce, but not busier than needed.
func flushInterval(debounce time.Duration) time.Duration {
	d := debounce / 2
	if d < 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	return d
}
