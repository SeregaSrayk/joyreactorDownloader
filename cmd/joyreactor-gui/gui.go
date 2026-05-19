package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~jackmordaunt/go-toast/v2"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"joyreactorDownloader/internal/client"
	"joyreactorDownloader/internal/downloader"
	"joyreactorDownloader/internal/filter"
	"joyreactorDownloader/internal/graphql"
)

// GUI is the Wails-bound struct. Every exported method is callable from JS.
type GUI struct {
	ctx   context.Context
	gql   *graphql.Client
	httpc *client.Client

	sessionPath string
	presets     *presetStore
	authors     *authorStore

	blockedMu   sync.RWMutex
	blockedTags []string

	jobs *jobManager
}

func NewGUI() *GUI { return &GUI{} }

func (g *GUI) startup(ctx context.Context) {
	g.ctx = ctx
	g.gql = buildGqlClient(loadAppSettings())
	g.httpc = client.New()
	g.sessionPath = sessionFilePath()
	g.presets = loadPresets()
	g.authors = loadAuthors()
	g.jobs = newJobManager(g)
	_ = g.gql.LoadSession(g.sessionPath)
	// Best-effort background refresh of the blocked-tag list (if session was restored).
	go g.refreshBlockedTags()
	// JR rotates session cookies on subsequent requests — without periodic
	// flush the disk copy goes stale, and on next launch we'd restore an
	// invalidated cookie and appear logged out. Flush every 5 min while
	// logged in; the on-shutdown hook covers normal app close.
	go g.sessionPersistLoop()
	// Background scheduler that watches presets opted into AutoPull and
	// enqueues jobs whenever the global interval has elapsed.
	go g.schedulerLoop()
	// If the user enabled "start minimized" in settings, hide the window
	// after Wails finishes its startup so the app boots into the tray.
	// The tray icon itself is started in main() before wails.Run() — see
	// the comment there for why.
	if loadAppSettings().StartMinimized {
		go func() {
			// Defer past startup so the runtime context is alive and
			// WindowHide actually has something to hide.
			time.Sleep(200 * time.Millisecond)
			wailsruntime.WindowHide(g.ctx)
		}()
	}
}

// shutdown is wired as the Wails OnShutdown callback; it flushes the most
// recent session cookies to disk so the next launch sees what JR has
// rotated to during this run, not whatever was current at login time.
// The tray icon is stopped in main() after wails.Run() returns.
func (g *GUI) shutdown(_ context.Context) {
	if g.sessionPath != "" {
		_ = g.gql.SaveSession(g.sessionPath)
	}
}

// onBeforeClose is wired as the Wails OnBeforeClose callback. When the
// user clicks the window's X (or Alt+F4 / Cmd+W) and MinimizeToTrayOnClose
// is enabled in settings, we hide the window to the tray instead of
// quitting the process. The tray menu's "Выход" remains the only way to
// fully exit when this option is on.
//
// Returning true tells Wails "I handled the close, do NOT proceed with
// shutdown"; false lets the normal exit path run.
func (g *GUI) onBeforeClose(_ context.Context) bool {
	if trayIsQuitting() {
		return false
	}
	if !loadAppSettings().MinimizeToTrayOnClose {
		return false
	}
	wailsruntime.WindowHide(g.ctx)
	return true
}

// sessionPersistLoop periodically writes the current cookie jar to disk
// while the user is logged in. JR may rotate the session token on the
// server during normal API traffic; if we crash or are force-killed
// before shutdown runs, the on-disk copy would otherwise be stale.
func (g *GUI) sessionPersistLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-t.C:
			// Only persist when we actually have a session — avoids
			// overwriting on disk with an empty jar after the user
			// logged out from a different process.
			if name, _ := g.gql.Me(g.ctx); name != "" {
				_ = g.gql.SaveSession(g.sessionPath)
			}
		}
	}
}

// refreshBlockedTags fetches the authenticated user's blocked tag list and
// caches it for future Search/StartDownload merges. Safe to call when not
// logged in (returns empty silently).
func (g *GUI) refreshBlockedTags() {
	names, err := g.gql.BlockedTags(g.ctx)
	if err != nil {
		return
	}
	g.blockedMu.Lock()
	g.blockedTags = names
	g.blockedMu.Unlock()
	wailsruntime.EventsEmit(g.ctx, "auth:blocked-tags", len(names))
}

func (g *GUI) getBlockedTags() []string {
	g.blockedMu.RLock()
	defer g.blockedMu.RUnlock()
	out := make([]string, len(g.blockedTags))
	copy(out, g.blockedTags)
	return out
}

// BlockedTagCount returns how many tags the user has blocked in their profile.
// Zero if anonymous or not yet fetched.
func (g *GUI) BlockedTagCount() int {
	g.blockedMu.RLock()
	defer g.blockedMu.RUnlock()
	return len(g.blockedTags)
}

// ===== Auth =====

type LoginResult struct {
	Success  bool   `json:"success"`
	Username string `json:"username"`
	Error    string `json:"error,omitempty"`
}

func (g *GUI) Login(name, password string) LoginResult {
	u, err := g.gql.Login(g.ctx, name, password)
	if err != nil {
		return LoginResult{Error: err.Error()}
	}
	if err := g.gql.SaveSession(g.sessionPath); err != nil {
		wailsruntime.LogWarningf(g.ctx, "save session: %v", err)
	}
	go g.refreshBlockedTags()
	return LoginResult{Success: true, Username: u}
}

func (g *GUI) Logout() {
	_ = g.gql.Logout(g.ctx)
	_ = os.Remove(g.sessionPath)
	g.blockedMu.Lock()
	g.blockedTags = nil
	g.blockedMu.Unlock()
	wailsruntime.EventsEmit(g.ctx, "auth:blocked-tags", 0)
}

func (g *GUI) Me() string {
	name, _ := g.gql.Me(g.ctx)
	return name
}

// TagSuggest returns autocomplete suggestions for a tag mask.
// Empty mask or any error returns an empty slice (frontend treats it as "no suggestions").
func (g *GUI) TagSuggest(mask string) []graphql.TagSuggestion {
	if mask == "" {
		return []graphql.TagSuggestion{}
	}
	out, err := g.gql.TagAutocomplete(g.ctx, mask)
	if err != nil {
		return []graphql.TagSuggestion{}
	}
	return out
}

// UserCheck reports whether a username exists on Joyreactor.
type UserCheck struct {
	Found    bool    `json:"found"`
	Username string  `json:"username"`
	PostNum  int     `json:"postNum"`
	Rating   float64 `json:"rating"`
}

// CheckUser verifies an exact username via Query.user. Used by the GUI to give
// inline validation feedback under the "Автор" input.
func (g *GUI) CheckUser(name string) UserCheck {
	if name == "" {
		return UserCheck{}
	}
	info, err := g.gql.UserByName(g.ctx, name)
	if err != nil || info == nil {
		return UserCheck{}
	}
	return UserCheck{
		Found:    true,
		Username: info.Username,
		PostNum:  info.PostNum,
		Rating:   info.Rating,
	}
}

// SuggestUsers returns up to 10 usernames from the local cache (collected
// during prior searches) that case-insensitively contain mask, ordered by
// how often the user has appeared in past results.
func (g *GUI) SuggestUsers(mask string) []string {
	return g.authors.Suggest(mask, 10)
}

// CommentView is one comment in the post-preview overlay.
type CommentView struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Rating    float64   `json:"rating"`
	Level     int       `json:"level"`
	CreatedAt string    `json:"createdAt"`
	Author    string    `json:"author"`
	Pictures  []Picture `json:"pictures,omitempty"`
}

// PostCommentsView is what the frontend reads for the comments side-panel.
type PostCommentsView struct {
	CommentsCount int           `json:"commentsCount"`
	Comments      []CommentView `json:"comments"`
	Error         string        `json:"error,omitempty"`
}

// PostComments fetches the comment thread for a post by its Relay ID. Used by
// the right-click preview overlay.
func (g *GUI) PostComments(postID string) PostCommentsView {
	if postID == "" {
		return PostCommentsView{Error: "пустой postID"}
	}
	res, err := g.gql.PostComments(g.ctx, postID)
	if err != nil {
		return PostCommentsView{Error: err.Error()}
	}
	if res == nil {
		return PostCommentsView{}
	}
	out := PostCommentsView{
		CommentsCount: res.CommentsCount,
		Comments:      make([]CommentView, 0, len(res.Comments)),
	}
	for _, c := range res.Comments {
		cv := CommentView{
			ID:        c.ID,
			Text:      c.Text,
			Rating:    c.Rating,
			Level:     c.Level,
			CreatedAt: c.CreatedAt.Format(time.RFC3339),
			Author:    c.User.Username,
		}
		for _, a := range c.Attributes {
			// Comment attrs are stored under /pics/comment/ — JR's /pics/post/webm/
			// transcode URL doesn't apply, so pass empty slug.
			if pic, ok := buildPicture(a, ""); ok {
				cv.Pictures = append(cv.Pictures, pic)
			}
		}
		out.Comments = append(out.Comments, cv)
	}
	return out
}

// ===== Search =====

type SearchInput struct {
	Query          string   `json:"query"`
	Tags           []string `json:"tags"`
	ExcludeTags    []string `json:"excludeTags"`
	Username       string   `json:"username"`
	MinRating      *int     `json:"minRating"`
	MaxRating      *int     `json:"maxRating"`
	Sort           string   `json:"sort"`
	ShowNsfw       bool     `json:"showNsfw"`
	OnlyNsfw       bool     `json:"onlyNsfw"`
	ShowUnsafe     bool     `json:"showUnsafe"`
	OnlyFavorite   bool     `json:"onlyFavorite"`
	UseBlockedTags bool     `json:"useBlockedTags"`
	Page           int      `json:"page"`
}

type SearchOutput struct {
	Count int       `json:"count"`
	Posts []Preview `json:"posts"`
	Error string    `json:"error,omitempty"`
}

type Preview struct {
	PostID       string    `json:"postId"`
	PostNum      int64     `json:"postNum"`
	Rating       float64   `json:"rating"`
	CreatedAt    string    `json:"createdAt"`
	NSFW         bool      `json:"nsfw"`
	Removed      bool      `json:"removed"` // DMCA / takedown — pictures unavailable
	Author       string    `json:"author"`
	Tags         []string  `json:"tags"`
	ThumbnailURL string    `json:"thumbnailUrl"`
	Pictures     []Picture `json:"pictures"`
}

type Picture struct {
	AttrID   string `json:"attrId"`
	InsertID int    `json:"insertId,omitempty"`
	URL      string `json:"url"`
	VideoURL string `json:"videoUrl,omitempty"` // set when JR has a transcoded .mp4 (image.HasVideo)
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
}

func (g *GUI) Search(in SearchInput) SearchOutput {
	c := g.mergeBlockedTags(criteriaFromSearch(in), in.UseBlockedTags)
	settings := loadAppSettings()
	hideRemoved := settings.HideRemoved()
	page := in.Page
	if page < 1 {
		page = 1
	}
	res, err := g.gql.Search(g.ctx, buildSearchParams(c, page))
	if err != nil {
		return SearchOutput{Error: err.Error()}
	}
	if res.PostPager == nil {
		return SearchOutput{}
	}

	out := SearchOutput{Count: res.PostPager.Count}
	authors := make([]string, 0, len(res.PostPager.Posts))
	// Indices (within out.Posts) of removed previews we'll try to recover.
	// Collected in the main pass so we can run all onion fetches concurrently
	// after the cheap clearnet pass is done.
	var removedIdx []int
	var removedPosts []graphql.Post
	for _, p := range res.PostPager.Posts {
		if p.User.Username != "" {
			authors = append(authors, p.User.Username)
		}
		// Client-side: drop posts whose tag set intersects c.ExcludeTags
		// (which includes the user's profile-blocked tags when the toggle is on).
		// tagNamesForMatching expands aliases via Tag.mainTag.
		if !c.MatchPostTags(tagNamesForMatching(p)) {
			continue
		}
		removed := p.IsRemoved()
		// Recovery needs all three: opted in, mirror URL, and a working
		// SOCKS5 transport (since the mirror is a .onion the OS can't
		// resolve directly). Missing any one ⇒ fall through to the regular
		// "removed" presentation.
		recoveryActive := removed &&
			settings.RecoverDmcaViaOnion &&
			settings.OnionBaseURL != "" &&
			settings.Socks5Enabled
		// Hide-removed only kicks in when we have no chance of recovering —
		// otherwise we'd drop posts that we're about to fill in successfully.
		if removed && hideRemoved && !recoveryActive {
			continue
		}
		thumb, _ := p.ThumbnailURL()
		_, postNum, _ := graphql.DecodeID(p.ID)
		prev := Preview{
			PostID:       p.ID,
			PostNum:      postNum,
			Rating:       p.Rating,
			CreatedAt:    p.CreatedAt.Format(time.RFC3339),
			NSFW:         p.NSFW,
			Removed:      removed,
			Author:       p.User.Username,
			Tags:         tagNames(p),
			ThumbnailURL: thumb,
		}
		webmSlug := firstTagSlug(p)
		for _, a := range p.Attributes {
			if pic, ok := buildPicture(a, webmSlug); ok {
				prev.Pictures = append(prev.Pictures, pic)
			}
		}
		out.Posts = append(out.Posts, prev)
		if recoveryActive {
			removedIdx = append(removedIdx, len(out.Posts)-1)
			removedPosts = append(removedPosts, p)
		}
	}
	if len(removedIdx) > 0 {
		g.recoverViaOnion(out.Posts, removedIdx, removedPosts, settings, hideRemoved)
		// Filter out removed-and-still-unrecoverable posts when hideRemoved
		// is on. Doing it post-recovery keeps successful rescues visible.
		if hideRemoved {
			kept := out.Posts[:0]
			for _, p := range out.Posts {
				if p.Removed {
					continue
				}
				kept = append(kept, p)
			}
			out.Posts = kept
		}
	}
	if g.authors != nil {
		g.authors.RecordBatch(authors)
	}
	return out
}

// ===== Download =====

type DownloadInput struct {
	SearchInput

	MediaKinds     []string `json:"mediaKinds"`
	MinWidth       int      `json:"minWidth"`
	MinHeight      int      `json:"minHeight"`
	DateFrom       string   `json:"dateFrom"`
	DateTo         string   `json:"dateTo"`
	Limit          int      `json:"limit"`
	Workers        int      `json:"workers"`
	OutDir         string   `json:"outDir"`
	FilenameFormat string   `json:"filenameFormat"` // "id" (default) | "tags"

	// PageFrom / PageTo bound the Query.search.postPager iteration in
	// produce(). PageFrom <= 1 (or 0) means "start at page 1"; PageTo <= 0
	// means "no upper bound". Used to skip an already-fetched prefix or
	// constrain the run to a known-good range.
	PageFrom int `json:"pageFrom"`
	PageTo   int `json:"pageTo"`

	// SelectedItems, when non-empty, switches the job to a fan-out over those
	// specific posts instead of paginating Query.search. Search/criteria
	// filters (tags / nsfw / rating / size / etc.) are bypassed — the user
	// already made an explicit pick, so we trust them.
	SelectedItems []SelectedItem `json:"selectedItems,omitempty"`
}

// SelectedItem mirrors enough of a Preview to enqueue downloads without
// another GraphQL round-trip: each picture already has its CDN URL and we
// know the post id (for the filename) and the tag list (for the "tags"
// filename format).
type SelectedItem struct {
	PostID   string            `json:"postId"`
	Tags     []string          `json:"tags"`
	Pictures []SelectedPicture `json:"pictures"`
}

type SelectedPicture struct {
	AttrID string `json:"attrId"`
	URL    string `json:"url"`
	Type   string `json:"type"` // GraphQL ImageType string (PNG/JPEG/GIF/MP4/...)
}

type AddJobResult struct {
	ID    string `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
}

// AddJob enqueues a new download job. Jobs auto-start and run in parallel;
// the GraphQL client's internal mutex serializes API requests across them.
// `name` is the user-friendly label; pass "" for an auto-generated one.
func (g *GUI) AddJob(name string, in DownloadInput) AddJobResult {
	id, err := g.jobs.add(in, name)
	if err != nil {
		return AddJobResult{Error: err.Error()}
	}
	return AddJobResult{ID: id}
}

// ListJobs returns the current queue, oldest-first.
func (g *GUI) ListJobs() []JobView { return g.jobs.snapshot() }

// PauseJob pauses a running job.
func (g *GUI) PauseJob(id string) { g.jobs.pauseJob(id) }

// ResumeJob resumes a paused job.
func (g *GUI) ResumeJob(id string) { g.jobs.resumeJob(id) }

// CancelJob cancels a running or paused job.
func (g *GUI) CancelJob(id string) { g.jobs.cancelJob(id) }

// RemoveJob drops a finished job from the list. Returns "" on success or the
// error message string.
func (g *GUI) RemoveJob(id string) string {
	if err := g.jobs.removeJob(id); err != nil {
		return err.Error()
	}
	return ""
}

// ClearFinishedJobs removes all done/error/canceled jobs from the list and
// returns how many were removed.
func (g *GUI) ClearFinishedJobs() int { return g.jobs.clearFinished() }

// PreviewJobName returns the auto-generated label for a given input snapshot —
// the frontend uses it to suggest a default name in the "Add" form.
func (g *GUI) PreviewJobName(in DownloadInput) string { return defaultJobName(in) }

// ===== Downloaded manifest =====

// ManifestKeys returns the attribute IDs the manifest records as
// already-downloaded. The frontend uses the list to paint the green "have
// it" check on preview tiles. Path depends on AppSettings.ManifestMode:
// per-folder reads <outDir>/.manifest.json, global reads the shared file
// in the app config dir. Empty/missing → empty slice.
func (g *GUI) ManifestKeys(outDir string) []string {
	if outDir == "" {
		return []string{}
	}
	m, err := downloader.LoadManifestFile(manifestPathFor(outDir))
	if err != nil || m == nil {
		return []string{}
	}
	keys := make([]string, 0, len(m.Entries))
	for k := range m.Entries {
		keys = append(keys, k)
	}
	return keys
}

// ===== App-wide settings =====

// GetAppSettings returns the persisted application-wide preferences
// (currently just ManifestMode). Settings modal reads this on open.
func (g *GUI) GetAppSettings() AppSettings { return loadAppSettings() }

// SaveAppSettings writes preferences to settings.json. Returns "" on
// success or the error message. Change takes effect for the next job;
// already-running jobs keep their downloader instances unchanged.
//
// As a side effect, the Autostart flag is synced to the OS-level
// autostart registration (Windows registry, macOS LaunchAgent,
// Linux .desktop) — this keeps the user's "should I auto-launch?"
// preference in a single canonical place.
func (g *GUI) SaveAppSettings(s AppSettings) string {
	if err := saveAppSettings(s); err != nil {
		return err.Error()
	}
	if err := syncAutostart(s.Autostart); err != nil {
		return "настройки сохранены, но автозапуск не удалось обновить: " + err.Error()
	}
	return ""
}

// OpenManifestFolder opens the directory that holds the active manifest
// in the OS file manager. Active path depends on AppSettings.ManifestMode:
// per-folder mode opens outDir, global mode opens the app config dir.
// Returns "" on success or the error message.
func (g *GUI) OpenManifestFolder(outDir string) string {
	dir, err := activeManifestFolder(outDir)
	if err != nil {
		return err.Error()
	}
	return g.OpenOutputFolder(dir)
}

// DeleteManifest removes the active manifest file (per current
// ManifestMode). After deletion, the green "downloaded" badges in the UI
// will disappear on the next refresh. Returns "" on success, error
// message otherwise. Missing file is NOT an error — idempotent.
func (g *GUI) DeleteManifest(outDir string) string {
	settings := loadAppSettings()
	var path string
	if settings.ManifestMode == "global" {
		path = globalManifestPath()
	} else {
		if outDir == "" {
			return "укажи папку для скачивания, чтобы найти манифест"
		}
		path = filepath.Join(outDir, ".manifest.json")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err.Error()
	}
	return ""
}

// RebuildResult — outcome of RebuildManifest. Added is how many new
// keys landed in the manifest, Removed is how many stale entries were
// dropped because their files no longer exist on disk, Scanned is the
// number of files whose names matched one of the supported formats,
// Inspected is the total number of files the walk visited (Scanned +
// names that didn't match — useful for sanity-checking that recursion
// reached deep folders). Error is "" on success.
type RebuildResult struct {
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Scanned   int    `json:"scanned"`
	Inspected int    `json:"inspected"`
	Error     string `json:"error"`
}

// RebuildManifest walks scanDir recursively, parses filenames in any
// format recognised by downloader.parseAttrID (id / tags / joysave /
// seo), and rewrites the active manifest so it contains exactly the
// attribute IDs found on disk. Stale entries — including, in "global"
// ManifestMode, entries that belong to other folders not being scanned
// — are deleted. The caller is expected to confirm the destructiveness
// in the UI.
func (g *GUI) RebuildManifest(scanDir string) RebuildResult {
	if scanDir == "" {
		return RebuildResult{Error: "укажи папку для сканирования"}
	}
	info, err := os.Stat(scanDir)
	if err != nil {
		return RebuildResult{Error: err.Error()}
	}
	if !info.IsDir() {
		return RebuildResult{Error: "путь не является папкой: " + scanDir}
	}
	m, err := downloader.LoadManifestFile(manifestPathFor(scanDir))
	if err != nil {
		return RebuildResult{Error: err.Error()}
	}
	stats, err := m.RebuildFromDir(scanDir)
	res := RebuildResult{
		Added:     stats.Added,
		Removed:   stats.Removed,
		Scanned:   stats.Scanned,
		Inspected: stats.Inspected,
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

// activeManifestFolder resolves the parent directory of the active
// manifest. Mirrors manifestPathFor's logic.
func activeManifestFolder(outDir string) (string, error) {
	if loadAppSettings().ManifestMode == "global" {
		return filepath.Dir(globalManifestPath()), nil
	}
	if outDir == "" {
		return "", errors.New("укажи папку для скачивания, чтобы найти манифест")
	}
	return outDir, nil
}

// ===== Window =====

// GetWindowSettings returns the persisted window dimensions + fullscreen flag.
// The frontend uses this to populate the Settings modal.
func (g *GUI) GetWindowSettings() WindowSettings {
	return loadWindowSettings()
}

// SaveWindowSettings persists the requested window settings to disk AND
// applies them to the live window so the user sees the change immediately.
// Returns the error message ("" on success).
//
// "Maximized" = fills the OS work area, taskbar stays visible. Not the same
// as Wails's true Fullscreen mode (which hides OS chrome) — we deliberately
// don't use that here.
func (g *GUI) SaveWindowSettings(ws WindowSettings) string {
	// Persist first so a crash mid-apply doesn't lose the user's choice.
	if err := saveWindowSettings(ws); err != nil {
		return err.Error()
	}
	if ws.Maximized {
		wailsruntime.WindowMaximise(g.ctx)
	} else {
		wailsruntime.WindowUnmaximise(g.ctx)
		if ws.Width > 0 && ws.Height > 0 {
			wailsruntime.WindowSetSize(g.ctx, ws.Width, ws.Height)
		}
	}
	return ""
}

// PickFolder opens a native folder picker and returns the chosen path
// (empty string if the user cancels).
func (g *GUI) PickFolder() string {
	path, err := wailsruntime.OpenDirectoryDialog(g.ctx, wailsruntime.OpenDialogOptions{
		Title: "Папка для скачивания",
	})
	if err != nil {
		return ""
	}
	return path
}

// OpenOutputFolder opens path in the OS file manager (Explorer on Windows,
// Finder on macOS, the default GUI handler on Linux via xdg-open).
// Returns the error message string for easy frontend display (empty = success).
func (g *GUI) OpenOutputFolder(path string) string {
	if path == "" {
		return "пустой путь"
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		// Linux / *BSD — every modern desktop ships xdg-utils.
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	return ""
}

// ===== Presets =====

func (g *GUI) ListPresets() []string { return g.presets.Names() }

func (g *GUI) GetPreset(name string) *Preset {
	p, ok := g.presets.Get(name)
	if !ok {
		return nil
	}
	return &p
}

func (g *GUI) SavePreset(name string, p Preset) string {
	if err := g.presets.Save(name, p); err != nil {
		return err.Error()
	}
	return ""
}

func (g *GUI) DeletePreset(name string) string {
	if err := g.presets.Delete(name); err != nil {
		return err.Error()
	}
	return ""
}

// SetPresetAutoPull toggles a preset's opt-in flag for the background
// scheduler. Returns "" on success or the error message.
func (g *GUI) SetPresetAutoPull(name string, on bool) string {
	if err := g.presets.SetAutoPull(name, on); err != nil {
		return err.Error()
	}
	return ""
}

// ===== internals =====

func fireToast(title, body, outDir string) {
	n := toast.Notification{
		AppID: "Joyreactor Downloader",
		Title: title,
		Body:  body,
	}
	if outDir != "" {
		n.Actions = []toast.Action{
			{Content: "Открыть папку", Arguments: outDir},
		}
	}
	_ = n.Push()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// produce paginates Query.search and emits jobs into the channel.
// (Duplicated from internal/app, intentionally — keeping the GUI's own pipeline
// so we can later layer per-page UI updates without changing the CLI pipeline.)
//
// settings is snapshotted at job-start time and controls whether DMCA-removed
// posts are recovered via the onion mirror. Non-removed posts always go through
// regular clearnet GraphQL; removed posts go through the mirror only when
// RecoverDmcaViaOnion + Socks5Enabled + OnionBaseURL are all set. Failure to
// recover (no SOCKS, mirror down, post not on mirror) falls through to the
// existing "skip" behaviour, so a flaky mirror never blocks the rest of the
// job.
func produce(ctx context.Context, gql *graphql.Client, c filter.Criteria, dl *downloader.Downloader, jobs chan<- downloader.Job, pause *pauseGate, nameFormat string, settings AppSettings) error {
	seen := make(map[string]struct{})
	sent := 0
	// Page range honours Criteria.PageFrom / PageTo. PageFrom <=1 (or 0)
	// behaves like the historical "start at 1"; PageTo <=0 means "no upper
	// bound, paginate until the API runs out of posts".
	startPage := c.PageFrom
	if startPage < 1 {
		startPage = 1
	}
	recoveryActive := settings.RecoverDmcaViaOnion &&
		settings.OnionBaseURL != "" &&
		settings.Socks5Enabled
	for page := startPage; ; page++ {
		if c.PageTo > 0 && page > c.PageTo {
			return nil
		}
		if err := pause.Wait(ctx); err != nil {
			return err
		}
		res, err := gql.Search(ctx, buildSearchParams(c, page))
		if err != nil {
			return fmt.Errorf("search page %d: %w", page, err)
		}
		if res.PostPager == nil || len(res.PostPager.Posts) == 0 {
			return nil
		}
		// Per-page onion recovery for DMCA-stubbed posts. Done before the main
		// loop so we can splice recovered attributes back into the iteration
		// transparently. Posts that fail to recover stay absent from the map
		// and fall through to the regular "skip removed" branch.
		var recoveredAttrs map[string][]graphql.Attribute
		if recoveryActive {
			var removed []graphql.Post
			for _, p := range res.PostPager.Posts {
				if p.IsRemoved() {
					removed = append(removed, p)
				}
			}
			recoveredAttrs = recoverAttrsViaOnion(ctx, removed, settings)
		}
		for _, p := range res.PostPager.Posts {
			attrs := p.Attributes
			if p.IsRemoved() {
				r, ok := recoveredAttrs[p.ID]
				if !ok {
					continue
				}
				attrs = r
			}
			if !c.MatchPostDate(p.CreatedAt) {
				continue
			}
			if !c.MatchPostTags(tagNamesForMatching(p)) {
				continue
			}
			for _, a := range attrs {
				if a.Type != graphql.AttrPicture || a.Image == nil {
					continue
				}
				if _, dup := seen[a.ID]; dup {
					continue
				}
				if !c.MatchImage(a.Image.Width, a.Image.Height, mediaKindOf(a.Image.Type)) {
					continue
				}
				url, err := a.FileURL()
				if err != nil {
					continue
				}
				seen[a.ID] = struct{}{}
				job := downloader.Job{
					URL:  url,
					Name: buildFilename(p, a, nameFormat),
					Key:  a.ID,
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case jobs <- job:
					sent++
					if c.Limit > 0 && sent >= c.Limit {
						return nil
					}
				}
			}
		}
		_ = dl // dl is reserved for future per-page UI events
	}
}

func buildSearchParams(c filter.Criteria, page int) graphql.SearchParams {
	p := graphql.SearchParams{
		Query: c.Query, TagNames: c.Tags, Username: c.Username,
		MinRating: c.MinRating, MaxRating: c.MaxRating, Page: page,
	}
	// Inclusion flags must be sent explicitly (true *and* false). If we send
	// null when the user unchecks them, the server falls back to the account's
	// profile preference, which makes the UI toggle look broken.
	showNsfw := c.ShowNsfw
	showUnsafe := c.ShowUnsafe
	p.ShowNsfw = &showNsfw
	p.ShowUnsafe = &showUnsafe

	// "Only X" filters are restrictive: only meaningful when true. Sending
	// false would tell the server "explicitly NOT only-X", which is the same
	// as omitting the flag — leave them off when unchecked.
	t := true
	if c.OnlyNsfw {
		p.ShowOnlyNsfw = &t
	}
	if c.OnlyFavorite {
		p.SearchInMyFavorites = &t
	}
	switch c.Sort {
	case filter.SortRating:
		p.SortByRating = &t
	case filter.SortDate:
		p.SortByDate = &t
	}
	return p
}

func criteriaFromSearch(in SearchInput) filter.Criteria {
	return filter.Criteria{
		Query:        in.Query,
		Tags:         in.Tags,
		ExcludeTags:  in.ExcludeTags,
		Username:     in.Username,
		MinRating:    in.MinRating,
		MaxRating:    in.MaxRating,
		Sort:         parseSort(in.Sort),
		ShowNsfw:     in.ShowNsfw,
		OnlyNsfw:     in.OnlyNsfw,
		ShowUnsafe:   in.ShowUnsafe,
		OnlyFavorite: in.OnlyFavorite,
	}
}

// mergeBlockedTags injects the cached profile-blocked tags into c.ExcludeTags
// if the user opted in. Deduped, preserving the user's explicit exclusions.
func (g *GUI) mergeBlockedTags(c filter.Criteria, use bool) filter.Criteria {
	if !use {
		return c
	}
	blocked := g.getBlockedTags()
	if len(blocked) == 0 {
		return c
	}
	have := make(map[string]struct{}, len(c.ExcludeTags))
	for _, t := range c.ExcludeTags {
		have[t] = struct{}{}
	}
	for _, t := range blocked {
		if _, ok := have[t]; !ok {
			c.ExcludeTags = append(c.ExcludeTags, t)
			have[t] = struct{}{}
		}
	}
	return c
}

func criteriaFromDownload(in DownloadInput) filter.Criteria {
	c := criteriaFromSearch(in.SearchInput)
	c.MediaKinds = parseMediaKinds(in.MediaKinds)
	c.MinWidth = in.MinWidth
	c.MinHeight = in.MinHeight
	c.DateFrom, _ = parseDate(in.DateFrom)
	c.DateTo, _ = parseDate(in.DateTo)
	c.Limit = in.Limit
	c.PageFrom = in.PageFrom
	c.PageTo = in.PageTo
	return c
}

// parseMediaKinds converts the frontend's list of kind strings into filter
// values. Empty list, or a list containing "any", means "no kind filter".
func parseMediaKinds(in []string) []filter.MediaKind {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[filter.MediaKind]struct{}, len(in))
	out := make([]filter.MediaKind, 0, len(in))
	for _, s := range in {
		k := filter.MediaKind(s)
		switch k {
		case filter.MediaAny:
			return nil
		case filter.MediaImage, filter.MediaGIF, filter.MediaVideo:
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

func parseSort(s string) filter.SortMode {
	switch s {
	case "date":
		return filter.SortDate
	case "rating", "":
		return filter.SortRating
	default:
		return filter.SortRating
	}
}

func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02", s)
}

func tagNames(p graphql.Post) []string {
	out := make([]string, len(p.Tags))
	for i, t := range p.Tags {
		out[i] = t.Name
	}
	return out
}

// tagNamesForMatching returns both the literal tag names and their mainTag
// names — JR groups variant tags under a canonical one via Tag.mainTag,
// so a profile-blocked canonical tag should also catch its variants.
func tagNamesForMatching(p graphql.Post) []string {
	out := make([]string, 0, len(p.Tags)*2)
	seen := make(map[string]struct{}, len(p.Tags)*2)
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, t := range p.Tags {
		add(t.Name)
		if t.MainTag != nil {
			add(t.MainTag.Name)
		}
	}
	return out
}

func mediaKindOf(t graphql.ImageType) filter.MediaKind {
	switch t {
	case graphql.ImageGIF:
		return filter.MediaGIF
	case graphql.ImageMP4, graphql.ImageWEBM:
		return filter.MediaVideo
	default:
		return filter.MediaImage
	}
}

// buildPicture converts a GraphQL picture attribute into the frontend's
// Picture view. Skips non-picture attributes and ones missing an image.
//
// For animated GIFs on PostAttributePicture, when webmSlug is non-empty we
// populate VideoURL with the URL of JR's webm transcode at
// /pics/post/webm/<webmSlug>-<attrId>.webm. The slug should be the first
// post tag's seoName — JR generates one transcode per post and the path
// depends on which tag JR happened to pick. Wrong slug ⇒ 404; the frontend
// falls back to the original .gif when <video> errors out.
func buildPicture(a graphql.Attribute, webmSlug string) (Picture, bool) {
	if a.Type != graphql.AttrPicture || a.Image == nil {
		return Picture{}, false
	}
	url, err := a.FileURL()
	if err != nil {
		return Picture{}, false
	}
	pic := Picture{
		AttrID:   a.ID,
		InsertID: a.InsertID,
		URL:      url,
		Width:    a.Image.Width,
		Height:   a.Image.Height,
		Type:     string(a.Image.Type),
		Kind:     string(mediaKindOf(a.Image.Type)),
	}
	if webmSlug != "" && a.Image.IsAnimated() && a.Image.Type == graphql.ImageGIF {
		if v, err := a.WebmURL(webmSlug); err == nil {
			pic.VideoURL = v
		}
	}
	return pic, true
}

// firstTagSlug picks the post's primary tag-slug for building webm transcode
// URLs. JR's seoName is the canonical url-safe form (transliterated for
// Cyrillic tags) and is what shows up in tag URLs on the site. We don't fall
// back to Name because the raw (often Cyrillic) name doesn't match JR's
// path — better to skip the webm and let the gif render as a still <img>.
func firstTagSlug(p graphql.Post) string {
	for _, t := range p.Tags {
		if t.SeoName == "" {
			continue
		}
		return url.PathEscape(strings.ToLower(t.SeoName))
	}
	return ""
}

// buildFilename produces the on-disk filename for a single picture attribute.
// format selects the naming scheme; "id" (or any unknown value) is the default
// numeric form, "tags" prefixes the post's tags joined by underscore. The post
// id + attribute id always appear at the tail so files stay unique even when
// two posts share the same tag set.
func buildFilename(p graphql.Post, a graphql.Attribute, format string) string {
	return buildFilenamePrim(p.ID, a.ID, string(a.Image.Type), tagNames(p), format)
}

// buildFilenamePrim is the GraphQL-agnostic core of buildFilename — used by
// the selected-items download path, which doesn't have a graphql.Post in hand
// (the frontend already provided the picture metadata directly).
func buildFilenamePrim(postID, attrID, imageType string, tags []string, format string) string {
	postNum := numOr(postID)
	attrNum := numOr(attrID)
	ext := lower(imageType)
	base := fmt.Sprintf("%s_%s.%s", postNum, attrNum, ext)
	switch format {
	case "tags":
		tagPart := joinTagsForName(tags, 6, 150)
		if tagPart == "" {
			return base
		}
		return fmt.Sprintf("%s_%s_%s.%s", tagPart, postNum, attrNum, ext)
	case "joysave":
		return buildJoySaveFilename(postNum, attrNum, tags, ext)
	default:
		return base
	}
}

// buildJoySaveFilename emits a filename byte-for-byte compatible with corax4's
// JoySave (joysave_main.pas:1131-1156), so a folder shared between the two
// tools converges on the same on-disk names:
//
//	<postNum>_0_<attrNum padded to 9 zeros>__<tag1>-<tag2>-<tag3>-<tag4>.<ext>
//
// The '_0_' slot is JoySave's "post vs comment" marker — '0' for pictures
// inside a post body, '1' for pictures attached to a comment. Our download
// pipeline only consumes Post.Attributes from Query.search (no comment
// pictures in scope), so '0' is hardcoded; switch to '1' if we ever surface
// comment attachments in mass downloads.
//
// Tags follow JoySave's default mode (cbAllTagsToName unchecked): up to 4
// first tags in their original order — NOT alphabetically sorted, intentionally,
// so two downloads of the same post produce identical filenames only when the
// API returns tags in the same order. JoySave accepts the same caveat. Spaces
// inside a tag become '-'. The alternative "all tags joined with '=' " JoySave
// mode is not exposed here.
//
// Finally Windows-reserved punctuation (\/:*?|<>") is replaced with '@' across
// the whole assembled filename, matching JoySave's post-process sweep.
func buildJoySaveFilename(postNum, attrNum string, tags []string, ext string) string {
	padded := padLeftZeros(attrNum, 9)
	var b strings.Builder
	b.WriteString(postNum)
	b.WriteString("_0_")
	b.WriteString(padded)
	b.WriteString("__")
	for i, t := range tags {
		if i >= 4 {
			break
		}
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(strings.ReplaceAll(t, " ", "-"))
	}
	b.WriteByte('.')
	b.WriteString(ext)
	return sanitizeWindowsFilename(b.String())
}

// padLeftZeros pads s on the left with '0' until it reaches at least n bytes.
// Returns s unchanged when it is already at least n bytes long. Used to
// reproduce JoySave's `while length(ImgId) < 9 do ImgId := '0' + ImgId` loop.
func padLeftZeros(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return strings.Repeat("0", n-len(s)) + s
}

// sanitizeWindowsFilename replaces every char that Windows rejects in a path
// component with '@', mirroring JoySave's joysave_main.pas:1148-1156 sweep.
// Used by the joysave filename format so the produced name is identical to
// what JoySave would write for the same post + picture.
func sanitizeWindowsFilename(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\', '/', ':', '*', '?', '|', '<', '>', '"':
			b.WriteRune('@')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// produceSelected fans the user's hand-picked items into the download queue.
// No Search call, no client-side filters — the user already chose, so the
// only dedup is against picture IDs we've already emitted (in case the
// caller passed the same post twice). dl's manifest still skips on-disk
// duplicates downstream.
func produceSelected(ctx context.Context, items []SelectedItem, dl *downloader.Downloader, jobs chan<- downloader.Job, pause *pauseGate, format string) error {
	_ = dl // reserved for future per-item UI events, mirrors produce()
	seen := make(map[string]struct{})
	for _, it := range items {
		if err := pause.Wait(ctx); err != nil {
			return err
		}
		for _, pic := range it.Pictures {
			if pic.AttrID == "" || pic.URL == "" {
				continue
			}
			if _, dup := seen[pic.AttrID]; dup {
				continue
			}
			seen[pic.AttrID] = struct{}{}
			job := downloader.Job{
				URL:  pic.URL,
				Name: buildFilenamePrim(it.PostID, pic.AttrID, pic.Type, it.Tags, format),
				Key:  pic.AttrID,
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case jobs <- job:
			}
		}
	}
	return nil
}

// joinTagsForName builds a `[tag1][tag2][tag3]` filename segment from the
// post's tags. Tags are alphabetically sorted so the same post always
// produces the same name even when GraphQL returns the tag list in a
// different order. Brackets make tag boundaries unambiguous — `art_cat_dog`
// could be 1/2/3 tags, but `[art][cat][dog]` is always three. Drops empty
// tags, caps per-tag and total length (in runes, so Cyrillic doesn't get
// truncated mid-character), and trims at a `]` boundary so a leftover
// truncated tag fragment doesn't sneak in.
func joinTagsForName(tags []string, maxTags, maxLen int) string {
	const perTagMax = 40
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		s := sanitizeTagForName(t)
		if s == "" {
			continue
		}
		if rs := []rune(s); len(rs) > perTagMax {
			s = string(rs[:perTagMax])
		}
		parts = append(parts, s)
	}
	sort.Strings(parts)
	if len(parts) > maxTags {
		parts = parts[:maxTags]
	}
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('[')
		b.WriteString(p)
		b.WriteByte(']')
	}
	out := b.String()
	if rs := []rune(out); len(rs) > maxLen {
		truncated := string(rs[:maxLen])
		if i := strings.LastIndex(truncated, "]"); i >= 0 {
			out = truncated[:i+1]
		} else {
			out = truncated
		}
	}
	return out
}

// sanitizeTagForName replaces filesystem-unsafe characters so the tag can
// sit inside `[...]` in a filename on Windows/macOS/Linux. Brackets in the
// tag itself get demoted to parentheses (they'd break the bracket scheme),
// and the usual reserved punctuation is squashed to `-`. Spaces inside
// brackets are safe and readable so we keep them.
func sanitizeTagForName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '[':
			b.WriteRune('(')
		case ']':
			b.WriteRune(')')
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('-')
		default:
			if r < 0x20 {
				continue
			}
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func numOr(gid string) string {
	_, n, err := graphql.DecodeID(gid)
	if err != nil {
		return "x"
	}
	return fmt.Sprintf("%d", n)
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out[i] = c
	}
	return string(out)
}

func max1(n int) int {
	if n < 1 {
		return 4
	}
	return n
}

func sessionFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "session.json"
	}
	return filepath.Join(dir, "joyreactorDownloader", "session.json")
}
