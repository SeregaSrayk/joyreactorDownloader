package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"joyreactorDownloader/internal/client"
	"joyreactorDownloader/internal/downloader"
	"joyreactorDownloader/internal/filter"
	"joyreactorDownloader/internal/graphql"
)

// Run drives the full pipeline: Query.search → client-side filters →
// downloader.Job stream → worker pool.
func Run(ctx context.Context, c filter.Criteria, outDir string, workers int) error {
	if workers < 1 {
		workers = 1
	}
	gql := graphql.NewClient("")
	httpc := client.New()
	dl := downloader.New(httpc, outDir)

	jobs := make(chan downloader.Job, workers*2)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				ok, err := dl.Fetch(ctx, job)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						log.Printf("download %s: %v", job.Name, err)
					}
					continue
				}
				if ok {
					log.Printf("saved %s", job.Name)
				} else {
					log.Printf("skip %s (exists)", job.Name)
				}
			}
		}()
	}

	produceErr := produce(ctx, gql, c, jobs)
	close(jobs)
	wg.Wait()

	if produceErr != nil && !errors.Is(produceErr, context.Canceled) {
		return produceErr
	}
	return nil
}

// produce paginates Search results, applies client-side filters, and emits
// downloader.Job into jobs. Returns when no more posts, limit hit, or ctx done.
func produce(ctx context.Context, gql *graphql.Client, c filter.Criteria, jobs chan<- downloader.Job) error {
	seen := make(map[string]struct{})
	sent := 0

	for page := 1; ; page++ {
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
				img := *a.Image
				if _, dup := seen[a.ID]; dup {
					continue
				}
				if !c.MatchImage(img.Width, img.Height, mediaKindOf(img.Type)) {
					continue
				}
				url, err := a.FileURL()
				if err != nil {
					log.Printf("file url: %v", err)
					continue
				}
				seen[a.ID] = struct{}{}

				job := downloader.Job{URL: url, Name: filename(p.ID, a.ID, img.Type), Key: a.ID}
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
	}
}

func buildSearchParams(c filter.Criteria, page int) graphql.SearchParams {
	p := graphql.SearchParams{
		Query:     c.Query,
		TagNames:  c.Tags,
		Username:  c.Username,
		MinRating: c.MinRating,
		MaxRating: c.MaxRating,
		Page:      page,
	}
	// Inclusion flags (showNsfw/showUnsafe) must be sent explicitly: null
	// falls back to the logged-in account's profile preference, which makes
	// the flag look ignored when unset.
	showNsfw := c.ShowNsfw
	showUnsafe := c.ShowUnsafe
	p.ShowNsfw = &showNsfw
	p.ShowUnsafe = &showUnsafe

	// Restrictive flags are meaningful only when true.
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

func tagNames(p graphql.Post) []string {
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

// filename builds "<postNum>_<imageNum>.<ext>". Falls back to raw IDs if decode fails.
func filename(postID, imageID string, ext graphql.ImageType) string {
	pNum := decodeNumOr(postID)
	iNum := decodeNumOr(imageID)
	return fmt.Sprintf("%s_%s.%s", pNum, iNum, strings.ToLower(string(ext)))
}

func decodeNumOr(gid string) string {
	_, n, err := graphql.DecodeID(gid)
	if err != nil {
		return sanitize(gid)
	}
	return fmt.Sprintf("%d", n)
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", "=", "")
	return r.Replace(s)
}
