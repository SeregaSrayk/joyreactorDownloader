package graphql

import (
	"strings"
	"time"
)

type ImageType string

const (
	ImagePNG  ImageType = "PNG"
	ImageJPEG ImageType = "JPEG"
	ImageGIF  ImageType = "GIF"
	ImageBMP  ImageType = "BMP"
	ImageTIFF ImageType = "TIFF"
	ImageMP4  ImageType = "MP4"
	ImageWEBM ImageType = "WEBM"
	ImageWEBP ImageType = "WEBP"
)

type AttributeType string

const (
	AttrPicture    AttributeType = "PICTURE"
	AttrYouTube    AttributeType = "YOUTUBE"
	AttrVimeo      AttributeType = "VIMEO"
	AttrCoub       AttributeType = "COUB"
	AttrSoundcloud AttributeType = "SOUNDCLOUD"
	AttrBandcamp   AttributeType = "BANDCAMP"
)

type PostLineType string

const (
	LineAll  PostLineType = "ALL"
	LineNew  PostLineType = "NEW"
	LineGood PostLineType = "GOOD"
	LineBest PostLineType = "BEST"
)

type Image struct {
	ID       string    `json:"id"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	Type     ImageType `json:"type"`
	HasVideo bool      `json:"hasVideo"`
}

type Attribute struct {
	Typename string        `json:"__typename"`
	ID       string        `json:"id"`
	Type     AttributeType `json:"type"`
	InsertID int           `json:"insertId"`
	Image    *Image        `json:"image,omitempty"`
	Value    string        `json:"value,omitempty"`
}

type Tag struct {
	Name string `json:"name"`
	// SeoName is JR's url-safe slug for the tag (typically lowercased latin,
	// transliterated for Cyrillic). Equal to Name for most English tags.
	// Used to build webm transcode URLs at /pics/post/webm/<seoName>-<attrId>.webm.
	SeoName string `json:"seoName,omitempty"`
	// MainTag is JR's canonical tag for this variant — JR groups variant
	// spellings (Latin/Cyrillic, casing) under a single canonical tag.
	// Self-pointing for canonical tags themselves.
	// Used by ExcludeTags matching to catch aliased variants of a blocked tag.
	MainTag *struct {
		Name string `json:"name"`
	} `json:"mainTag,omitempty"`
}

// TagSuggestion is one autocomplete result. Count is the number of posts under
// the tag; NSFW indicates whether the tag is flagged as adult.
type TagSuggestion struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	NSFW  bool   `json:"nsfw"`
}

// UserInfo is the subset of User used for the GUI's "user exists?" lookup.
type UserInfo struct {
	Username string  `json:"username"`
	PostNum  int     `json:"postNum"`
	Rating   float64 `json:"rating"`
}

type User struct {
	Username string `json:"username"`
}

type Post struct {
	ID         string      `json:"id"`
	Rating     float64     `json:"rating"`
	CreatedAt  time.Time   `json:"createdAt"`
	NSFW       bool        `json:"nsfw"`
	Unsafe     bool        `json:"unsafe"`
	Text       string      `json:"text"`
	User       User        `json:"user"`
	Tags       []Tag       `json:"tags"`
	Attributes []Attribute `json:"attributes"`
}

// IsRemoved reports whether the post was taken down (DMCA / copyright complaint).
// JR's tell: the text body is replaced with a /censorship/ placeholder image and
// the attributes array is emptied. Thumbnail still works because it's generated
// from the post id, not regenerated when the content is removed.
func (p Post) IsRemoved() bool {
	return len(p.Attributes) == 0 && strings.Contains(p.Text, "/censorship/")
}

type PostPager struct {
	Count int    `json:"count"`
	Posts []Post `json:"posts"`
}

// Comment is a single comment on a post. Level is the threading depth: 0 for
// top-level, 1 for direct replies, and so on. Comments come back flat in
// display order — use Level for visual indentation in the UI.
type Comment struct {
	ID         string      `json:"id"`
	Text       string      `json:"text"`
	Rating     float64     `json:"rating"`
	Level      int         `json:"level"`
	CreatedAt  time.Time   `json:"createdAt"`
	User       User        `json:"user"`
	Attributes []Attribute `json:"attributes"`
}
