package main

import (
	"fmt"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// scheduler watches presets opted into AutoPull and enqueues a fresh
// download job for each one when the configured interval has elapsed.
//
// Cadence is global (AppSettings.AutoPullIntervalHours, default 24h);
// opt-in is per-preset (Preset.AutoPull). The scheduler ticks every minute
// to keep wakeup cost low; the actual eligibility check is on the preset
// timestamps, not the tick rate.
//
// Edge cases:
//   - App not running at the scheduled time → on next launch, the preset
//     becomes eligible immediately (we don't try to "catch up" by running
//     multiple times; one pull suffices for the typical use case of
//     "download whatever's new since last run", which the manifest already
//     dedupes against).
//   - Anonymous session at run time → skip; the same preset may have
//     auth-gated filters (favorites, etc.), and we don't want to hammer
//     the API with no benefit.
//   - Manual job for the same preset already running → still enqueue;
//     jobs are isolated and the manifest dedupes inside the worker pool.
//   - LastAutoPullAt is set BEFORE the job spawns (not after completion)
//     so a crash mid-job doesn't trigger an immediate retry — we wait the
//     full interval again, matching the user's expectation of "once per
//     interval, at most".
func (g *GUI) schedulerLoop() {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	// First tick after 15s so a freshly-launched app catches up on any
	// presets that became due while the app was closed.
	first := time.NewTimer(15 * time.Second)
	defer first.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-first.C:
			g.runDueAutoPulls()
		case <-t.C:
			g.runDueAutoPulls()
		}
	}
}

func (g *GUI) runDueAutoPulls() {
	settings := loadAppSettings()
	intervalH := settings.AutoPullIntervalHours
	if intervalH < 1 {
		intervalH = 24
	}
	interval := time.Duration(intervalH) * time.Hour
	now := time.Now()

	// Authenticated state check is cheap (uses cookie cache, doesn't hit JR
	// unless the cookie was just rotated); skip the whole pass if we have
	// no session.
	if name, _ := g.gql.Me(g.ctx); name == "" {
		return
	}

	for presetName, p := range g.presets.All() {
		if !p.AutoPull {
			continue
		}
		if !p.LastAutoPullAt.IsZero() && now.Sub(p.LastAutoPullAt) < interval {
			continue
		}
		// Convert preset snapshot to a DownloadInput and enqueue.
		// FilenameFormat is a global AppSettings preference (not stored
		// per-preset), so we apply it here on every spawn — that way
		// changing the format in the UI affects the next scheduled run
		// for every preset, no per-preset config touch needed.
		in := downloadInputFromPreset(p)
		in.FilenameFormat = settings.FilenameFormatOrDefault()
		if in.OutDir == "" {
			// Preset has no outDir — can't auto-pull. Notify the user once
			// per cycle and keep moving; flipping AutoPull off would be
			// disruptive without confirmation.
			wailsruntime.LogWarningf(g.ctx, "auto-pull skipped for preset %q: no outDir", presetName)
			continue
		}
		jobName := fmt.Sprintf("%s · auto", presetName)
		_, err := g.jobs.add(in, jobName)
		if err != nil {
			wailsruntime.LogWarningf(g.ctx, "auto-pull spawn failed for %q: %v", presetName, err)
			continue
		}
		// Mark intent right after spawn so a subsequent crash doesn't loop.
		_ = g.presets.MarkAutoPullStarted(presetName, now)
	}
}

// downloadInputFromPreset converts the persisted Preset shape into the
// DownloadInput the job manager expects. Mirrors what the frontend would
// build when loading a preset and pressing «Добавить в очередь».
func downloadInputFromPreset(p Preset) DownloadInput {
	return DownloadInput{
		SearchInput: SearchInput{
			Query:        p.Query,
			Tags:         append([]string(nil), p.Tags...),
			ExcludeTags:  append([]string(nil), p.ExcludeTags...),
			Username:     p.Username,
			MinRating:    p.MinRating,
			MaxRating:    p.MaxRating,
			Sort:         p.Sort,
			Feed:         p.Feed,
			ShowNsfw:     p.ShowNsfw,
			OnlyNsfw:     p.OnlyNsfw,
			ShowUnsafe:   p.ShowUnsafe,
			OnlyFavorite: p.OnlyFavorite,
			// Auto-pulls always respect the user's profile-blocked tags;
			// there's no UI toggle to disable that for scheduled runs.
			UseBlockedTags: true,
		},
		MediaKinds: append([]string(nil), p.MediaKinds...),
		MinWidth:   p.MinWidth,
		MinHeight:  p.MinHeight,
		DateFrom:   p.DateFrom,
		DateTo:     p.DateTo,
		Limit:      p.Limit,
		Workers:    p.Workers,
		OutDir:     p.OutDir,
		PageFrom:   p.PageFrom,
		PageTo:     p.PageTo,
	}
}
