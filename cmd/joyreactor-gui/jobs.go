package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"joyreactorDownloader/internal/downloader"
)

// Job states.
const (
	StateRunning  = "running"
	StatePaused   = "paused"
	StateDone     = "done"
	StateError    = "error"
	StateCanceled = "canceled"
)

// JobView is the JSON-friendly snapshot sent to the frontend on every change.
type JobView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OutDir    string `json:"outDir"`
	State     string `json:"state"`
	Saved     int64  `json:"saved"`
	Skipped   int64  `json:"skipped"`
	Failed    int64  `json:"failed"`
	Last      string `json:"last,omitempty"`
	Error     string `json:"error,omitempty"`
	// LastErr is the message of the most recent per-picture Fetch
	// failure (HTTP error, write error, etc.). Distinct from Error,
	// which is the whole-job fatal error. Surfaced in the queue UI
	// next to the ✖ counter so the user can see why downloads are
	// failing without digging through wails logs.
	LastErr   string `json:"lastErr,omitempty"`
	Limit     int    `json:"limit"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   int64  `json:"endedAt,omitempty"`
}

// job is the internal record. Unexported fields are excluded from JSON, but
// we never serialize this — frontend gets JobView via .view().
type job struct {
	id     string
	name   string
	outDir string
	limit  int
	input  DownloadInput

	ctx    context.Context
	cancel context.CancelFunc
	pause  *pauseGate

	mu      sync.RWMutex
	state   string
	last    string
	errMsg  string
	lastErr string // most recent per-picture Fetch error, surfaced in UI next to the ✖ counter
	start   time.Time
	end     time.Time

	saved   atomic.Int64
	skipped atomic.Int64
	failed  atomic.Int64
}

func (j *job) view() JobView {
	j.mu.RLock()
	defer j.mu.RUnlock()
	v := JobView{
		ID: j.id, Name: j.name, OutDir: j.outDir,
		State:     j.state,
		Last:      j.last,
		Error:     j.errMsg,
		LastErr:   j.lastErr,
		Limit:     j.limit,
		StartedAt: j.start.UnixMilli(),
		Saved:     j.saved.Load(),
		Skipped:   j.skipped.Load(),
		Failed:    j.failed.Load(),
	}
	if !j.end.IsZero() {
		v.EndedAt = j.end.UnixMilli()
	}
	return v
}

func (j *job) setState(s string) {
	j.mu.Lock()
	j.state = s
	if s == StateDone || s == StateError || s == StateCanceled {
		if j.end.IsZero() {
			j.end = time.Now()
		}
	}
	j.mu.Unlock()
}

func (j *job) finished() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.state == StateDone || j.state == StateError || j.state == StateCanceled
}

// jobManager owns all download jobs. All jobs run concurrently; the GraphQL
// client's internal mutex serializes search/metadata requests across them.
type jobManager struct {
	g     *GUI
	mu    sync.RWMutex
	jobs  map[string]*job
	order []string
}

func newJobManager(g *GUI) *jobManager {
	return &jobManager{g: g, jobs: map[string]*job{}}
}

func (m *jobManager) snapshot() []JobView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]JobView, 0, len(m.order))
	for _, id := range m.order {
		if j, ok := m.jobs[id]; ok {
			out = append(out, j.view())
		}
	}
	return out
}

func (m *jobManager) get(id string) *job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.jobs[id]
}

func (m *jobManager) add(input DownloadInput, name string) (string, error) {
	if input.OutDir == "" {
		return "", errors.New("укажи папку для скачивания")
	}
	if name == "" {
		name = defaultJobName(input)
	}
	// Selected-items mode has a deterministic total — every picture across
	// every picked post. Treat it as the limit so the progress bar can be
	// determinate. Any user-provided Limit is overridden because explicit
	// selection already bounds the work.
	if len(input.SelectedItems) > 0 {
		total := 0
		for _, it := range input.SelectedItems {
			total += len(it.Pictures)
		}
		input.Limit = total
	}
	id := fmt.Sprintf("j-%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(m.g.ctx)
	j := &job{
		id: id, name: name, outDir: input.OutDir,
		limit: input.Limit, input: input,
		ctx: ctx, cancel: cancel, pause: newPauseGate(),
		start: time.Now(),
		state: StateRunning,
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.order = append(m.order, id)
	m.mu.Unlock()
	m.emitUpdate(j)
	go m.run(j)
	return id, nil
}

func (m *jobManager) emitUpdate(j *job) {
	wailsruntime.EventsEmit(m.g.ctx, "job:update", j.view())
}

func (m *jobManager) emitRemoved(id string) {
	wailsruntime.EventsEmit(m.g.ctx, "job:removed", id)
}

func (m *jobManager) run(j *job) {
	workers := max1(j.input.Workers)
	// Snapshot settings at job-start time so a toggle mid-run can't shift
	// the manifest scope or the folder-split chunk size halfway through.
	settings := loadAppSettings()
	// Manifest scope follows AppSettings.ManifestMode. Resolved at job-start
	// time so toggling the setting takes effect for new jobs but doesn't
	// disturb anything already running.
	dl := downloader.NewWithManifest(m.g.httpc, j.outDir, manifestPathFor(j.outDir))
	if settings.FolderSplitEvery > 0 {
		dl.EnableSplit(settings.FolderSplitEvery, downloader.SplitUnit(settings.FolderSplitUnitOrDefault()))
	}
	queue := make(chan downloader.Job, workers*2)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dljob := range queue {
				if err := j.pause.Wait(j.ctx); err != nil {
					return
				}
				ok, err := dl.Fetch(j.ctx, dljob)
				switch {
				case err != nil:
					if !errors.Is(err, context.Canceled) {
						j.failed.Add(1)
						msg := err.Error()
						j.mu.Lock()
						j.lastErr = msg
						j.mu.Unlock()
						wailsruntime.LogWarningf(m.g.ctx, "job %s: %v", j.id, err)
					}
				case ok:
					j.saved.Add(1)
				default:
					j.skipped.Add(1)
				}
				j.mu.Lock()
				j.last = dljob.Name
				j.mu.Unlock()
				m.emitUpdate(j)
			}
		}()
	}

	var err error
	if len(j.input.SelectedItems) > 0 {
		// User-picked items — bypass Search, no client-side filters.
		err = produceSelected(j.ctx, j.input.SelectedItems, dl, queue, j.pause, j.input.FilenameFormat)
	} else {
		c := m.g.mergeBlockedTags(criteriaFromDownload(j.input), j.input.UseBlockedTags)
		err = produce(j.ctx, m.g.gql, c, dl, queue, j.pause, j.input.FilenameFormat, settings)
	}
	close(queue)
	wg.Wait()

	switch {
	case errors.Is(err, context.Canceled):
		j.setState(StateCanceled)
	case err != nil:
		j.mu.Lock()
		j.errMsg = err.Error()
		j.mu.Unlock()
		j.setState(StateError)
	default:
		j.setState(StateDone)
	}
	m.emitUpdate(j)

	title := "Готово: " + j.name
	body := summaryTextForJob(j)
	switch j.state {
	case StateCanceled:
		title = "Отменено: " + j.name
	case StateError:
		title = "Ошибка: " + j.name
		body = truncate(j.errMsg, 120)
	}
	fireToast(title, body, j.outDir)
}

func summaryTextForJob(j *job) string {
	s, sk, f := j.saved.Load(), j.skipped.Load(), j.failed.Load()
	if f > 0 {
		return fmt.Sprintf("Сохранено %d, пропущено %d, ошибок %d", s, sk, f)
	}
	return fmt.Sprintf("Сохранено %d, пропущено %d", s, sk)
}

func (m *jobManager) pauseJob(id string) {
	j := m.get(id)
	if j == nil {
		return
	}
	j.pause.Pause()
	j.setState(StatePaused)
	m.emitUpdate(j)
}

func (m *jobManager) resumeJob(id string) {
	j := m.get(id)
	if j == nil {
		return
	}
	j.pause.Resume()
	j.setState(StateRunning)
	m.emitUpdate(j)
}

func (m *jobManager) cancelJob(id string) {
	j := m.get(id)
	if j == nil {
		return
	}
	j.pause.Resume() // unblock paused waits so they observe ctx.Done
	j.cancel()
}

func (m *jobManager) removeJob(id string) error {
	j := m.get(id)
	if j == nil {
		return errors.New("задача не найдена")
	}
	if !j.finished() {
		return errors.New("сначала отмени задачу")
	}
	m.mu.Lock()
	delete(m.jobs, id)
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	m.emitRemoved(id)
	return nil
}

func (m *jobManager) clearFinished() int {
	m.mu.Lock()
	var keep []string
	var removed []string
	for _, id := range m.order {
		j, ok := m.jobs[id]
		if !ok {
			continue
		}
		if j.finished() {
			delete(m.jobs, id)
			removed = append(removed, id)
		} else {
			keep = append(keep, id)
		}
	}
	m.order = keep
	m.mu.Unlock()
	for _, id := range removed {
		m.emitRemoved(id)
	}
	return len(removed)
}

func defaultJobName(in DownloadInput) string {
	if n := len(in.SelectedItems); n > 0 {
		label := fmt.Sprintf("Выбранные ×%d", n)
		if base := filepath.Base(in.OutDir); base != "" && base != "." {
			label += " → " + base
		}
		return label
	}
	var parts []string
	if in.Query != "" {
		parts = append(parts, `"`+in.Query+`"`)
	}
	for _, t := range in.Tags {
		parts = append(parts, "#"+t)
	}
	if in.Username != "" {
		parts = append(parts, "@"+in.Username)
	}
	if in.MinRating != nil {
		parts = append(parts, fmt.Sprintf("≥%d", *in.MinRating))
	}
	if in.MaxRating != nil {
		parts = append(parts, fmt.Sprintf("≤%d", *in.MaxRating))
	}
	for _, k := range in.MediaKinds {
		if k != "" && k != "any" {
			parts = append(parts, k)
		}
	}
	label := strings.Join(parts, " ")
	if label == "" {
		label = "поиск"
	}
	if base := filepath.Base(in.OutDir); base != "" && base != "." {
		label += " → " + base
	}
	return label
}
