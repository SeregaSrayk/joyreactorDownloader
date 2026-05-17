//go:build integration

package graphql

import (
	"context"
	"testing"
	"time"
)

// Run with: go test -tags=integration ./internal/graphql/...
// Hits the live JoyReactor GraphQL endpoint — requires network.

func TestSearch_Live(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := NewClient("")
	minR := 100
	sortRating := true
	res, err := c.Search(ctx, SearchParams{
		Query:        "",
		TagNames:     []string{"art"},
		MinRating:    &minR,
		SortByRating: &sortRating,
		Page:         1,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.PostPager == nil {
		t.Fatal("PostPager is nil")
	}
	if res.PostPager.Count == 0 {
		t.Fatal("Count is 0")
	}
	if len(res.PostPager.Posts) == 0 {
		t.Fatal("no posts returned")
	}

	first := res.PostPager.Posts[0]
	if first.ID == "" {
		t.Error("post id empty")
	}
	if first.CreatedAt.IsZero() {
		t.Error("createdAt unparsed (zero time)")
	}
	if first.Rating < 100 {
		t.Errorf("rating %.2f < requested minRating 100", first.Rating)
	}

	hasPic := false
	for _, a := range first.Attributes {
		if a.Type == AttrPicture && a.Image != nil {
			hasPic = true
			url, err := a.FileURL()
			if err != nil {
				t.Errorf("FileURL: %v", err)
			}
			if url == "" {
				t.Error("FileURL empty")
			}
			break
		}
	}
	if !hasPic {
		t.Logf("first post has no PICTURE attribute (typenames: %v)", attrTypenames(first.Attributes))
	}
}

func attrTypenames(as []Attribute) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Typename
	}
	return out
}
