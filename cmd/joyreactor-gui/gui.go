package main

import (
	"context"
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
	g.gql = graphql.NewClient("")
	g.httpc = client.New()
	g.sessionPath = sessionFilePath()
	g.presets = loadPresets()
	g.authors = loadAuthors()
	g.jobs = newJobManager(g)
	_ = g.gql.LoadSession(g.sessionPath)
	// Best-effort background refresh of the blocked-tag list (if session was restored).
	go g.refreshBlockedTags()
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
	for _, p := range res.PostPager.Posts {
		if p.User.Username != "" {
			authors = append(authors, p.User.Username)
		}
		// Client-side: drop posts whose tag set intersects c.ExcludeTags
		// (which includes the user's profile-blocked tags when the toggle is on).
		if !c.MatchPostTags(tagNames(p)) {
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
func (g *GUI) SaveAppSettings(s AppSettings) string {
	if err := saveAppSettings(s); err != nil {
		return err.Error()
	}
	return ""
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
func produce(ctx context.Context, gql *graphql.Client, c filter.Criteria, dl *downloader.Downloader, jobs chan<- downloader.Job, pause *pauseGate, nameFormat string) error {
	seen := make(map[string]struct{})
	sent := 0
	for page := 1; ; page++ {
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
		for _, p := range res.PostPager.Posts {
			if !c.MatchPostDate(p.CreatedAt) {
				continue
			}
			if !c.MatchPostTags(tagNames(p)) {
				continue
			}
			for _, a := range p.Attributes {
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
	if format != "tags" {
		return base
	}
	tagPart := joinTagsForName(tags, 6, 150)
	if tagPart == "" {
		return base
	}
	return fmt.Sprintf("%s_%s_%s.%s", tagPart, postNum, attrNum, ext)
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
