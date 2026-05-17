package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"joyreactorDownloader/internal/app"
	"joyreactorDownloader/internal/filter"
)

func main() {
	var (
		query    = flag.String("query", "", "free-text search query")
		user     = flag.String("user", "", "filter by post author (username)")
		nsfw     = flag.Bool("nsfw", false, "include NSFW posts")
		onlyNsfw = flag.Bool("only-nsfw", false, "show only NSFW posts")
		unsafe   = flag.Bool("unsafe", false, "include unsafe posts")
		favorite = flag.Bool("favorite", false, "search only in the authenticated user's favorites")
		sort     = flag.String("sort", "rating", "sort order: rating | date")

		kind      = flag.String("kind", "any", "media kind: image | gif | video | any")
		minWidth  = flag.Int("min-width", 0, "minimum image width (px)")
		minHeight = flag.Int("min-height", 0, "minimum image height (px)")

		from = flag.String("from", "", "earliest post date YYYY-MM-DD (client-side)")
		to   = flag.String("to", "", "latest post date YYYY-MM-DD (client-side)")

		out     = flag.String("out", "./downloads", "output directory")
		limit   = flag.Int("limit", 0, "max files to download (0 = no limit)")
		workers = flag.Int("workers", 4, "parallel download workers")
	)

	var tags, excludeTags stringSlice
	flag.Var(&tags, "tag", "tag to include (repeatable: -tag art -tag gif)")
	flag.Var(&excludeTags, "exclude-tag", "tag to exclude (repeatable)")

	var minRating, maxRating optionalInt
	flag.Var(&minRating, "min-rating", "minimum post rating (default: -inf)")
	flag.Var(&maxRating, "max-rating", "maximum post rating (default: +inf)")

	flag.Parse()

	dateFrom, err := parseDate(*from)
	if err != nil {
		exitErr("--from: %v", err)
	}
	dateTo, err := parseDate(*to)
	if err != nil {
		exitErr("--to: %v", err)
	}

	sortMode, err := parseSort(*sort)
	if err != nil {
		exitErr("--sort: %v", err)
	}

	mediaKind, err := parseKind(*kind)
	if err != nil {
		exitErr("--kind: %v", err)
	}

	criteria := filter.Criteria{
		Query:        *query,
		Tags:         tags,
		ExcludeTags:  excludeTags,
		Username:     *user,
		MinRating:    minRating.v,
		MaxRating:    maxRating.v,
		Sort:         sortMode,
		ShowNsfw:     *nsfw,
		OnlyNsfw:     *onlyNsfw,
		ShowUnsafe:   *unsafe,
		OnlyFavorite: *favorite,
		MediaKinds:   kindsFor(mediaKind),
		MinWidth:     *minWidth,
		MinHeight:    *minHeight,
		DateFrom:     dateFrom,
		DateTo:       dateTo,
		Limit:        *limit,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx, criteria, *out, *workers); err != nil {
		log.Fatalf("run failed: %v", err)
	}
}

// stringSlice is a repeatable string flag (e.g. -tag a -tag b).
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

// optionalInt is a flag whose presence is meaningful (vs. the zero value).
type optionalInt struct{ v *int }

func (o *optionalInt) String() string {
	if o == nil || o.v == nil {
		return ""
	}
	return strconv.Itoa(*o.v)
}

func (o *optionalInt) Set(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	o.v = &n
	return nil
}

func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02", s)
}

func parseSort(s string) (filter.SortMode, error) {
	switch s {
	case "", "rating":
		return filter.SortRating, nil
	case "date":
		return filter.SortDate, nil
	default:
		return "", fmt.Errorf("unknown sort %q (want: rating | date)", s)
	}
}

func parseKind(s string) (filter.MediaKind, error) {
	switch filter.MediaKind(s) {
	case filter.MediaAny, filter.MediaImage, filter.MediaGIF, filter.MediaVideo:
		return filter.MediaKind(s), nil
	default:
		return "", fmt.Errorf("unknown kind %q (want: image | gif | video | any)", s)
	}
}

func kindsFor(k filter.MediaKind) []filter.MediaKind {
	if k == "" || k == filter.MediaAny {
		return nil
	}
	return []filter.MediaKind{k}
}

func exitErr(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	flag.Usage()
	os.Exit(2)
}
