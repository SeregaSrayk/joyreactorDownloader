package filter

import "time"

type MediaKind string

const (
	MediaAny   MediaKind = "any"
	MediaImage MediaKind = "image"
	MediaGIF   MediaKind = "gif"
	MediaVideo MediaKind = "video"
)

type SortMode string

const (
	SortDefault SortMode = ""
	SortRating  SortMode = "rating"
	SortDate    SortMode = "date"
)

// Criteria describes what the user wants to download.
//
// Fields are split into three groups:
//   - GraphQL search params — passed straight to Query.search.
//   - Client-side filters — applied after fetching posts (the API has no equivalent).
//   - Run controls — limits and pagination.
type Criteria struct {
	// --- GraphQL search params (mirror Query.search args) ---
	Query        string
	Tags         []string
	ExcludeTags  []string
	Username     string
	MinRating    *int
	MaxRating    *int
	Sort         SortMode
	ShowNsfw     bool
	OnlyNsfw     bool
	ShowUnsafe   bool
	OnlyFavorite bool

	// --- client-side filters ---
	MediaKinds []MediaKind // empty (or contains MediaAny) = no kind filter
	MinWidth   int
	MinHeight  int
	DateFrom   time.Time // zero = unbounded
	DateTo     time.Time // zero = unbounded

	// --- run controls ---
	Limit    int // max files to download (0 = unlimited)
	PageFrom int // start search at this 1-based page (0 or 1 = start from beginning)
	PageTo   int // stop after this 1-based page, inclusive (0 = no upper bound)
}

// MatchImage applies client-side filters that depend only on a single image.
// kind is derived from ImageType by the caller (see KindOf in cmd/main wiring).
func (c Criteria) MatchImage(width, height int, kind MediaKind) bool {
	if c.MinWidth > 0 && width < c.MinWidth {
		return false
	}
	if c.MinHeight > 0 && height < c.MinHeight {
		return false
	}
	if !c.allowsKind(kind) {
		return false
	}
	return true
}

// allowsKind reports whether the image kind passes the MediaKinds filter.
// Empty MediaKinds or presence of MediaAny means all kinds pass.
func (c Criteria) allowsKind(kind MediaKind) bool {
	if len(c.MediaKinds) == 0 {
		return true
	}
	for _, k := range c.MediaKinds {
		if k == MediaAny || k == kind {
			return true
		}
	}
	return false
}

// MatchPostDate checks whether createdAt falls in [DateFrom, DateTo].
// Unset bounds are ignored.
func (c Criteria) MatchPostDate(createdAt time.Time) bool {
	if !c.DateFrom.IsZero() && createdAt.Before(c.DateFrom) {
		return false
	}
	if !c.DateTo.IsZero() && createdAt.After(c.DateTo) {
		return false
	}
	return true
}

// MatchPostTags returns false if the post carries any tag from ExcludeTags.
func (c Criteria) MatchPostTags(tagNames []string) bool {
	if len(c.ExcludeTags) == 0 {
		return true
	}
	bad := make(map[string]struct{}, len(c.ExcludeTags))
	for _, t := range c.ExcludeTags {
		bad[t] = struct{}{}
	}
	for _, t := range tagNames {
		if _, ok := bad[t]; ok {
			return false
		}
	}
	return true
}
