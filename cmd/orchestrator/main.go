// Command orchestrator watches the ZuidWest Update feed and posts new articles to X.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/oszuidwest/zw-xposter/internal/article"
	"github.com/oszuidwest/zw-xposter/internal/config"
	"github.com/oszuidwest/zw-xposter/internal/dedupe"
	"github.com/oszuidwest/zw-xposter/internal/feed"
	"github.com/oszuidwest/zw-xposter/internal/notify"
	"github.com/oszuidwest/zw-xposter/internal/poster"
	"github.com/oszuidwest/zw-xposter/internal/safehttp"
	"github.com/oszuidwest/zw-xposter/internal/state"
	operational "github.com/oszuidwest/zw-xposter/internal/status"
)

// Build metadata, set via -ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

const (
	maxTitleRunes    = 280 - 24 // X counts every link as 23 characters, plus the separating space.
	retryInitial     = 2 * time.Minute
	retryMaximum     = 30 * time.Minute
	uncertainMinimum = 5 * time.Minute
	// recentMargin is the X history checked beyond MAX_AGE.
	recentMargin          = 24 * time.Hour
	alertCheckInterval    = time.Minute
	operationalTimeout    = 10 * time.Second
	serverShutdownTimeout = 5 * time.Second
	// statusAddr is fixed because container/healthcheck.mjs expects it.
	statusAddr = "127.0.0.1:8080"

	posterNotReadyKey = "poster:not-ready"
	pollStaleKey      = "orchestrator:poll-stale"
)

type options struct {
	once   bool
	seed   bool
	replay string
}

type app struct {
	cfg                 *config.Config
	store               *state.Store
	http                *http.Client
	poster              *poster.Client
	now                 func() time.Time
	status              *operational.Tracker
	alerts              *notify.Service
	startedAt           time.Time
	posterNotReadySince time.Time
}

type pollState struct {
	now          time.Time
	lookback     time.Duration
	recent       []poster.Post
	recentLoaded bool
	attempted    bool
}

func main() {
	once := flag.Bool("once", false, "run a single poll and exit")
	seed := flag.Bool("seed", false, "initialize missing state with the current feed")
	replay := flag.String("replay", "", "reset one stored GUID for a duplicate-checked retry")
	flag.Parse()

	slog.Info("starting orchestrator", "version", Version, "commit", Commit, "build_time", BuildTime)

	if err := run(options{once: *once, seed: *seed, replay: *replay}, nil); err != nil {
		slog.Error("orchestrator stopped", "error", err)
		os.Exit(1)
	}
}

func run(opts options, contentHTTP *http.Client) error {
	if opts.seed && opts.replay != "" {
		return errors.New("-seed and -replay cannot be combined")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	lock, err := acquireInstanceLock(cfg.StateFile + ".lock")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	// Subtract the margin to check MAX_AGE + margin without overflow.
	if cfg.MaxAge > poster.MaxLookback-recentMargin {
		return fmt.Errorf("MAX_AGE must be at most %s, because the poster reads at most %s of X history, got %s",
			poster.MaxLookback-recentMargin, poster.MaxLookback, cfg.MaxAge)
	}

	if contentHTTP == nil {
		contentHTTP, err = safehttp.New(cfg.FeedURL, cfg.AllowedHosts)
		if err != nil {
			return fmt.Errorf("configure content HTTP client: %w", err)
		}
	}

	store, err := loadStore(cfg.StateFile, opts.seed)
	if err != nil {
		return err
	}
	a := &app{
		cfg:    cfg,
		store:  store,
		http:   contentHTTP,
		poster: poster.New(cfg.PosterURL),
		now:    time.Now,
		status: operational.New(3 * cfg.PollInterval),
	}
	slog.Info("config", "feed", cfg.FeedURL, "poster", cfg.PosterURL, "interval", cfg.PollInterval, "dry_run", cfg.DryRun)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case opts.seed:
		if err := a.seed(ctx); err != nil {
			return fmt.Errorf("seed state: %w", err)
		}
		return nil
	case opts.replay != "":
		return a.replay(ctx, opts.replay)
	}

	a.status.Refresh(store.Items)
	a.alerts = newAlerts(cfg)
	defer a.alerts.Close()

	if opts.once {
		return a.pollAndReport(ctx)
	}
	return a.serve(ctx, stop)
}

// newAlerts disables mail during dry runs, whose unsaved state would repeat notifications.
func newAlerts(cfg *config.Config) *notify.Service {
	switch {
	case cfg.DryRun:
		slog.Info("email alerting disabled during a dry run")
		return nil
	case !cfg.Graph.Complete():
		slog.Info("email alerting disabled; Microsoft Graph settings are incomplete")
	default:
		slog.Info("email alerting enabled", "recipients", len(cfg.Graph.Recipients), "reminder_interval", cfg.AlertReminder)
	}
	return notify.New(&cfg.Graph, cfg.AlertReminder)
}

func (a *app) serve(ctx context.Context, stop context.CancelFunc) error {
	// Detect port conflicts before a poll can publish anything.
	listener, err := net.Listen("tcp", statusAddr)
	if err != nil {
		return fmt.Errorf("status server: %w", err)
	}
	statusServer := &http.Server{
		Handler:           a.status.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// Buffer Serve's result so shutdown cannot strand its goroutine.
	serverErr := make(chan error, 1)
	go func() { serverErr <- statusServer.Serve(listener) }()
	a.startedAt = a.now()
	var monitor sync.WaitGroup
	monitor.Go(func() { a.monitorOperationalAlerts(ctx) })
	defer func() {
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		if err := statusServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("status server shutdown failed", "error", err)
		}
		monitor.Wait()
	}()

	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := a.pollAndReport(ctx); err != nil && ctx.Err() == nil {
			slog.Error("poll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return nil
		case err := <-serverErr:
			return fmt.Errorf("status server: %w", err)
		case <-ticker.C:
		}
	}
}

func (a *app) pollAndReport(ctx context.Context) error {
	if err := a.poll(ctx); err != nil {
		return err
	}
	a.status.RecordPoll(a.now())
	a.pingHeartbeat(ctx)
	return nil
}

// loadStore requires existing state for polling and missing state for seeding.
func loadStore(path string, seed bool) (*state.Store, error) {
	store, err := state.Load(path)
	if err != nil {
		return nil, err
	}
	switch {
	case seed && store.Exists():
		return nil, fmt.Errorf("state file %q already exists; -seed only initializes missing state", path)
	case !seed && !store.Exists():
		return nil, fmt.Errorf("state file %q is missing; initialize it with -seed (see README.md)", path)
	}
	return store, nil
}

// seed marks the current feed as handled to avoid posting the initial backlog.
func (a *app) seed(ctx context.Context) error {
	items, err := feed.Fetch(ctx, a.http, a.cfg.FeedURL)
	if err != nil {
		return err
	}
	now := a.now()
	for _, item := range items {
		a.store.SetAt(item.GUID, &state.Entry{
			Title:       item.Title,
			Link:        item.Link,
			PublishedAt: item.Published,
			Status:      state.StatusSeeded,
		}, now)
	}
	if err := a.save(); err != nil {
		return err
	}
	slog.Info("first run: marked current feed items as seen", "count", len(items), "dry_run", a.cfg.DryRun)
	return nil
}

func (a *app) replay(ctx context.Context, guid string) error {
	entry, ok := a.store.Items[guid]
	if !ok {
		return fmt.Errorf("replay GUID %q is not present in state", guid)
	}
	items, err := feed.Fetch(ctx, a.http, a.cfg.FeedURL)
	if err != nil {
		return fmt.Errorf("fetch current feed for replay: %w", err)
	}
	i := slices.IndexFunc(items, func(item feed.Item) bool { return item.GUID == guid })
	if i < 0 {
		return fmt.Errorf("replay GUID %q is not present in the current feed", guid)
	}
	now := a.now()
	if age := now.Sub(items[i].Published); age > poster.MaxLookback {
		return fmt.Errorf("replay GUID %q requires %d hours of X history; the poster maximum is %d hours",
			guid, poster.LookbackHours(age), poster.MaxLookbackHours)
	}

	retry := state.Entry{
		Title:             entry.Title,
		Link:              entry.Link,
		PublishedAt:       entry.PublishedAt,
		Status:            state.StatusRetry,
		ReplayRequestedAt: now.UTC(),
	}
	a.store.SetAt(guid, &retry, now)
	if err := a.save(); err != nil {
		return fmt.Errorf("save replay state: %w", err)
	}
	slog.Info("item reset for replay", "guid", guid, "title", retry.Title, "dry_run", a.cfg.DryRun)
	return nil
}

// recoverPosting marks unfinished attempts uncertain for reconciliation against X.
// Check each poll: crashes and failed outcome saves can leave posting entries.
func (a *app) recoverPosting() bool {
	changed := false
	now := a.now()
	for guid := range a.store.Items {
		entry := a.store.Items[guid]
		if entry.Status != state.StatusPosting {
			continue
		}
		entry.Status = state.StatusUncertain
		entry.LastError = "post outcome was not saved (orchestrator stopped or the state write failed)"
		entry.NextAttemptAt = now.Add(uncertainMinimum)
		a.store.SetAt(guid, &entry, now)
		changed = true
	}
	return changed
}

func (a *app) poll(ctx context.Context) error {
	if a.recoverPosting() {
		if err := a.save(); err != nil {
			return fmt.Errorf("save recovered state: %w", err)
		}
	}
	items, err := feed.Fetch(ctx, a.http, a.cfg.FeedURL)
	if err != nil {
		return err
	}

	now := a.now()
	// Expire unlisted items before a duplicate-check failure can block them.
	if err := a.expireUnlisted(items, now); err != nil {
		return err
	}
	workflow := &pollState{now: now, lookback: a.recentLookback(items, now)}
	for _, item := range items {
		if err := a.processItem(ctx, &item, workflow); err != nil {
			return err
		}
	}
	return nil
}

// expireUnlisted closes expired retries and uncertain outcomes outside the feed.
// Never post from state alone: missing articles may have been withdrawn.
// Without a publication date, only a replay within MAX_AGE keeps an entry pending.
func (a *app) expireUnlisted(items []feed.Item, now time.Time) error {
	listed := make(map[string]bool, len(items))
	for _, item := range items {
		listed[item.GUID] = true
	}
	for guid := range a.store.Items {
		if listed[guid] || a.store.Done(guid) {
			continue
		}
		entry := a.store.Items[guid]
		if !a.expired(&entry, now) {
			continue
		}
		if err := a.recordExpired(guid, &entry); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) processItem(ctx context.Context, item *feed.Item, workflow *pollState) error {
	if a.store.Done(item.GUID) {
		return nil
	}
	entry := a.store.Items[item.GUID]
	// The feed is authoritative for listed items' metadata, including replays.
	entry.Title, entry.Link, entry.PublishedAt = item.Title, item.Link, item.Published
	if a.expired(&entry, workflow.now) {
		return a.recordExpired(item.GUID, &entry)
	}
	if age := workflow.now.Sub(item.Published); age > workflow.lookback {
		// Only replays get here: unexpired items are within MAX_AGE, inside the lookback.
		entry.Status = state.StatusFailedTerminal
		entry.LastError = fmt.Sprintf("article is %d hours old, beyond the %d hours of X history the poster can check; check X manually",
			poster.LookbackHours(age), poster.MaxLookbackHours)
		slog.Error("article cannot be checked against X and needs manual review", "guid", item.GUID, "title", item.Title, "error", entry.LastError)
		return a.record(item.GUID, &entry)
	}
	if workflow.now.Before(eligibleAt(&entry)) {
		return nil
	}

	if !workflow.recentLoaded {
		// A complete timeline check is required to rule out duplicates.
		recent, err := a.poster.Recent(ctx, workflow.lookback)
		if err != nil {
			return fmt.Errorf("check X for existing posts: %w", err)
		}
		workflow.recent = recent
		workflow.recentLoaded = true
		a.status.RecordXCheck(a.now())
	}
	if existing := dedupe.Find(item.Link, workflow.recent); existing != nil {
		slog.Info("already on X, not posting", "title", item.Title, "post", existing.URL)
		entry.Status = state.StatusPosted
		entry.PostURL = existing.URL
		entry.FoundOnX = true
		entry.LastError = ""
		return a.record(item.GUID, &entry)
	}

	if workflow.attempted && !a.cfg.DryRun {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.cfg.PostDelay):
		}
	}
	url, err := a.publish(ctx, item, &entry)
	if err != nil {
		return err
	}
	if url != "" {
		workflow.recent = append(workflow.recent, poster.Post{URL: url, URLs: []string{item.Link}})
	}
	workflow.attempted = true
	return nil
}

// recentLookback covers MAX_AGE plus a margin and any checkable pending replay.
// Never truncate: the poster must reject an unsupported window, not check too little.
func (a *app) recentLookback(items []feed.Item, now time.Time) time.Duration {
	lookback := a.cfg.MaxAge + recentMargin
	for _, item := range items {
		if a.store.Done(item.GUID) {
			continue
		}
		entry := a.store.Items[item.GUID]
		if age := now.Sub(item.Published); !entry.ReplayRequestedAt.IsZero() && age <= poster.MaxLookback {
			lookback = max(lookback, age)
		}
	}
	return lookback
}

func (a *app) expired(entry *state.Entry, now time.Time) bool {
	start := entry.PublishedAt
	if entry.ReplayRequestedAt.After(start) {
		start = entry.ReplayRequestedAt
	}
	return now.Sub(start) > a.cfg.MaxAge
}

func (a *app) recordExpired(guid string, entry *state.Entry) error {
	switch entry.Status {
	case "": // not in state yet
		entry.Status = state.StatusMissed
		slog.Error("article became too old before it could be posted", "title", entry.Title, "published_at", entry.PublishedAt)
	case state.StatusUncertain:
		entry.Status = state.StatusFailedTerminal
		previousError := entry.LastError
		entry.LastError = "retry window expired while the outcome was uncertain; the post may already be on X and must be checked manually"
		if previousError != "" {
			entry.LastError += ": " + previousError
		}
		slog.Error("uncertain retry window expired; the post may already be on X and must be checked manually", "title", entry.Title, "attempts", entry.Attempts, "last_error", entry.LastError)
	default:
		entry.Status = state.StatusFailedTerminal
		slog.Error("retry window expired", "title", entry.Title, "attempts", entry.Attempts, "last_error", entry.LastError)
	}
	return a.record(guid, entry)
}

// publish posts the item and records the outcome in entry. Poster failures
// become workflow state; cancellation and state-save failures stop the poll.
func (a *app) publish(ctx context.Context, item *feed.Item, entry *state.Entry) (string, error) {
	log := slog.With("title", item.Title, "link", item.Link)
	text := postText(item)
	for {
		img, video, err := a.prepareMedia(ctx, item, entry)
		if err != nil {
			return "", err
		}
		// Release the potentially large file before any image/text fallback.
		closeVideo := func() {
			if video != nil {
				if err := video.Close(); err != nil {
					log.Warn("remove downloaded video", "error", err)
				}
			}
		}
		if err := ctx.Err(); err != nil {
			closeVideo()
			return "", err
		}
		if a.cfg.DryRun {
			// The API key is deliberately only passed to the poster process.
			log.Info("dry run: predicted post; captions, upload and browser outcome unverified", "text", text, "format", entry.Format, "fallback_reason", entry.FallbackReason)
			closeVideo()
			entry.Status = state.StatusPosted // in-memory preview only
			return "", a.record(item.GUID, entry)
		}

		entry.Status = state.StatusPosting
		entry.Attempts++
		entry.NextAttemptAt = time.Time{}
		if err := a.record(item.GUID, entry); err != nil {
			closeVideo()
			return "", fmt.Errorf("save write-ahead state: %w", err)
		}
		var result poster.Result
		var postErr error
		if err := ctx.Err(); err != nil {
			closeVideo()
			return "", err
		}
		if video != nil {
			result, postErr = a.poster.PostVideo(ctx, text, video, entry.Format == state.FormatVideoCaptions)
		} else {
			result.URL, postErr = a.poster.Post(ctx, text, img)
		}
		closeVideo()
		if video != nil && result.Captions == poster.CaptionsNone {
			entry.Format = state.FormatVideo
			if result.FallbackReason != "" {
				entry.FallbackReason = result.FallbackReason
			}
			log.Info("video captions skipped", "reason", entry.FallbackReason)
		}
		if postErr == nil {
			entry.Status, entry.PostURL, entry.LastError = state.StatusPosted, result.URL, ""
			log.Info("posted", "post", result.URL, "format", entry.Format, "captions", result.Captions, "fallback_reason", entry.FallbackReason)
			return result.URL, a.record(item.GUID, entry)
		}
		entry.LastError = postErr.Error()
		pe, known := errors.AsType[*poster.PostError](postErr)
		if next := fallbackFormat(entry.Format, pe); next != "" {
			if err := a.advanceFormat(ctx, item.GUID, entry, next, entry.LastError); err != nil {
				return "", err
			}
			continue
		}
		entry.Status = state.StatusRetry
		delay := retryDelay(entry.Attempts)
		// Only confirmed pre-click failures avoid the uncertain-outcome delay.
		if !known || pe.Clicked {
			entry.Status = state.StatusUncertain
			delay = max(delay, uncertainMinimum)
		}
		entry.NextAttemptAt = a.now().Add(delay)
		log.Warn("post failed; will reconcile before retrying", "status", entry.Status, "attempts", entry.Attempts, "next_attempt_at", entry.NextAttemptAt, "error", postErr)
		return "", a.record(item.GUID, entry)
	}
}

// Download failures and matching pre-click upload failures share this downgrade path.
var fallbackChain = map[string]struct{ stage, next string }{
	state.FormatVideoCaptions: {stage: poster.StageVideo, next: state.FormatImage},
	state.FormatVideo:         {stage: poster.StageVideo, next: state.FormatImage},
	state.FormatImage:         {stage: poster.StageImage, next: state.FormatText},
}

func fallbackFormat(format string, err *poster.PostError) string {
	if err == nil || err.Clicked {
		return ""
	}
	if step, ok := fallbackChain[format]; ok && err.Stage == step.stage {
		return step.next
	}
	return ""
}

// advanceFormat persists a downgrade before any work on the next format.
func (a *app) advanceFormat(ctx context.Context, guid string, entry *state.Entry, format, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	slog.Info("media fallback", "title", entry.Title, "from", entry.Format, "to", format, "reason", reason)
	entry.Format, entry.FallbackReason = format, reason
	entry.Status = state.StatusRetry
	if err := a.record(guid, entry); err != nil {
		return fmt.Errorf("save fallback state: %w", err)
	}
	return nil
}

// prepareMedia resumes the saved format and only downgrades.
func (a *app) prepareMedia(ctx context.Context, item *feed.Item, entry *state.Entry) (*article.Image, *article.Video, error) {
	if entry.Format == "" {
		entry.Format = state.FormatImage
		if item.VideoURL != "" {
			entry.Format = state.FormatVideoCaptions
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		var err error
		switch entry.Format {
		case state.FormatVideoCaptions, state.FormatVideo:
			var video *article.Video
			video, err = article.FetchVideo(ctx, a.http, item.VideoURL)
			if err == nil {
				return nil, video, nil
			}
		case state.FormatImage:
			var img *article.Image
			img, err = article.FetchImage(ctx, a.http, item.Link)
			if err == nil {
				return img, nil, nil
			}
		case state.FormatText:
			return nil, nil, nil
		default:
			return nil, nil, fmt.Errorf("unknown publication format %q", entry.Format)
		}
		if err := a.advanceFormat(ctx, item.GUID, entry, fallbackChain[entry.Format].next, err.Error()); err != nil {
			return nil, nil, err
		}
	}
}

func retryDelay(attempts int) time.Duration {
	delay := retryInitial
	for attempt := 1; attempt < attempts && delay < retryMaximum; attempt++ {
		delay *= 2
	}
	return min(delay, retryMaximum)
}

// eligibleAt enforces the uncertainty delay even if next_attempt_at is missing or early.
func eligibleAt(entry *state.Entry) time.Time {
	eligible := entry.NextAttemptAt
	if entry.Status == state.StatusUncertain {
		minimum := entry.UpdatedAt.Add(uncertainMinimum)
		if minimum.After(eligible) {
			eligible = minimum
		}
	}
	return eligible
}

// record saves the outcome, restoring the stored entry if the disk write fails.
// Final outcomes clear retry timing and replay markers.
func (a *app) record(guid string, entry *state.Entry) error {
	switch entry.Status {
	case state.StatusPosted, state.StatusMissed, state.StatusFailedTerminal:
		entry.NextAttemptAt = time.Time{}
		entry.ReplayRequestedAt = time.Time{}
	}
	previous, existed := a.store.Items[guid]
	a.store.SetAt(guid, entry, a.now())
	if err := a.save(); err != nil {
		if existed {
			a.store.Items[guid] = previous
		} else {
			delete(a.store.Items, guid)
		}
		return err
	}
	if event, ok := workflowAlert(guid, &previous, entry); ok {
		a.alerts.Notify(event)
	}
	return nil
}

func workflowAlert(guid string, previous, entry *state.Entry) (notify.Event, bool) {
	if previous.Status == entry.Status {
		return notify.Event{}, false
	}
	var summary string
	switch entry.Status {
	case state.StatusMissed:
		summary = "Article was missed"
	case state.StatusFailedTerminal:
		summary = "Article posting failed permanently"
	default:
		return notify.Event{}, false
	}
	details := fmt.Sprintf("GUID: %s\nTitle: %s\nLink: %s", guid, entry.Title, entry.Link)
	if entry.LastError != "" {
		details += "\nError: " + entry.LastError
	}
	return notify.Event{
		Key:     fmt.Sprintf("workflow:%s:%s", entry.Status, guid),
		Summary: summary,
		Details: details,
	}, true
}

func (a *app) monitorOperationalAlerts(ctx context.Context) {
	if !a.alerts.IsConfigured() {
		return
	}
	ticker := time.NewTicker(alertCheckInterval)
	defer ticker.Stop()
	for {
		a.evaluateOperationalAlerts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *app) evaluateOperationalAlerts(ctx context.Context) {
	now := a.now()
	checkCtx, cancel := context.WithTimeout(ctx, operationalTimeout)
	err := a.poster.Ready(checkCtx)
	cancel()
	if err == nil {
		a.posterNotReadySince = time.Time{}
		a.alerts.Resolve(notify.Event{
			Key:     posterNotReadyKey,
			Summary: "Poster is ready again",
			Details: "The cached X session check is healthy again.",
		})
	} else {
		if a.posterNotReadySince.IsZero() {
			a.posterNotReadySince = now
		}
		if now.Sub(a.posterNotReadySince) >= a.cfg.PosterNotReadyAfter {
			details := fmt.Sprintf("The poster has not been ready since %s. Error: %v", a.posterNotReadySince.UTC().Format(time.RFC3339), err)
			a.alerts.Alert(notify.Event{Key: posterNotReadyKey, Summary: "Poster is not ready", Details: details})
		}
	}

	snapshot := a.status.Snapshot()
	lastPoll := a.startedAt
	if snapshot.LastSuccessfulPoll != nil {
		lastPoll = *snapshot.LastSuccessfulPoll
	}
	if now.Sub(lastPoll) >= a.cfg.PollStaleAfter {
		a.alerts.Alert(notify.Event{
			Key:     pollStaleKey,
			Summary: "Orchestrator polls are stale",
			Details: fmt.Sprintf("No successful poll since %s.", lastPoll.UTC().Format(time.RFC3339)),
		})
	} else {
		a.alerts.Resolve(notify.Event{
			Key:     pollStaleKey,
			Summary: "Orchestrator polling recovered",
			Details: "A poll completed successfully again.",
		})
	}
}

func (a *app) pingHeartbeat(ctx context.Context) {
	if a.cfg.HeartbeatURL == "" || a.cfg.DryRun {
		return
	}
	heartbeatCtx, cancel := context.WithTimeout(ctx, operationalTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(heartbeatCtx, http.MethodGet, a.cfg.HeartbeatURL, http.NoBody)
	if err != nil {
		slog.Warn("heartbeat request could not be created", "error", err)
		return
	}
	// The heartbeat is operator-configured, so it skips the content restrictions.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("heartbeat failed", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		slog.Warn("heartbeat returned an error", "status", resp.Status)
	}
}

// save persists state and refreshes status; dry runs update status only.
func (a *app) save() error {
	if !a.cfg.DryRun {
		if err := a.store.Save(); err != nil {
			return err
		}
	}
	a.status.Refresh(a.store.Items)
	return nil
}

func postText(item *feed.Item) string {
	title := item.Title
	if runes := []rune(title); len(runes) > maxTitleRunes {
		title = string(runes[:maxTitleRunes-1]) + "…"
	}
	return title + " " + item.Link
}
