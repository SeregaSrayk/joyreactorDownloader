package graphql

import (
	"context"
	"sort"
)

const postFields = `
  id rating createdAt nsfw unsafe text
  user { username }
  tags { name seoName mainTag { name } }
  attributes {
    __typename id type
    ... on PostAttributePicture { image { id width height type hasVideo } }
    ... on PostAttributeEmbed    { value image { id width height type hasVideo } }
  }
`

const queryTagPosts = `
query TagPosts($name: String, $type: PostLineType!, $page: Int) {
  tag(name: $name) {
    postPager(type: $type) {
      count
      posts(page: $page) {` + postFields + `}
    }
  }
}`

const queryUserPosts = `
query UserPosts($username: String!, $page: Int) {
  user(username: $username) {
    postPager {
      count
      posts(page: $page) {` + postFields + `}
    }
  }
}`

const querySearch = `
query Search(
  $query: String!,
  $tagNames: [String!],
  $username: String,
  $showNsfw: Boolean,
  $showOnlyNsfw: Boolean,
  $showUnsafe: Boolean,
  $sortByDate: Boolean,
  $sortByRating: Boolean,
  $minRating: Int,
  $maxRating: Int,
  $searchInMyFavorites: Boolean,
  $page: Int
) {
  search(
    query: $query,
    tagNames: $tagNames,
    username: $username,
    showNsfw: $showNsfw,
    showOnlyNsfw: $showOnlyNsfw,
    showUnsafe: $showUnsafe,
    sortByDate: $sortByDate,
    sortByRating: $sortByRating,
    minRating: $minRating,
    maxRating: $maxRating,
    searchInMyFavorites: $searchInMyFavorites
  ) {
    excluded
    postPager {
      count
      posts(page: $page) {` + postFields + `}
    }
  }
}`

// SearchParams mirrors Query.search arguments. Nil pointers ⇒ unset (sent as null).
type SearchParams struct {
	Query               string
	TagNames            []string
	Username            string
	ShowNsfw            *bool
	ShowOnlyNsfw        *bool
	ShowUnsafe          *bool
	SortByDate          *bool
	SortByRating        *bool
	MinRating           *int
	MaxRating           *int
	SearchInMyFavorites *bool
	Page                int
}

type SearchResult struct {
	Excluded  bool       `json:"excluded"`
	PostPager *PostPager `json:"postPager"`
}

const queryTagAutocomplete = `
query TagAutocomplete($mask: String!) {
  tagAutocomplete(mask: $mask) { name count nsfw }
}`

const queryPostComments = `
query PostComments($id: ID!) {
  node(id: $id) {
    __typename
    ... on Post {
      id
      commentsCount
      comments {
        id text rating level createdAt
        user { username }
        attributes {
          __typename id type insertId
          ... on CommentAttributePicture { image { id width height type hasVideo } }
        }
      }
    }
  }
}`

// PostCommentsResult is the comments-only view of a post used by the GUI's
// post-preview overlay.
type PostCommentsResult struct {
	ID            string    `json:"id"`
	CommentsCount int       `json:"commentsCount"`
	Comments      []Comment `json:"comments"`
}

// PostComments fetches the comment thread for a single post by its Relay ID.
// Returns nil if the node is missing or isn't a Post.
func (c *Client) PostComments(ctx context.Context, postID string) (*PostCommentsResult, error) {
	var out struct {
		Node *PostCommentsResult `json:"node"`
	}
	if err := c.Do(ctx, queryPostComments, map[string]any{"id": postID}, &out); err != nil {
		return nil, err
	}
	return out.Node, nil
}

const queryUserByName = `
query UserByName($name: String!) {
  user(username: $name) { username postNum rating }
}`

const queryBlockedTags = `
{ me { blockedTags { name } } }`

// BlockedTags returns the authenticated user's profile-level blocked tag names.
// Returns an empty slice if not logged in (the server returns me=null).
func (c *Client) BlockedTags(ctx context.Context) ([]string, error) {
	var out struct {
		Me *struct {
			BlockedTags []struct {
				Name string `json:"name"`
			} `json:"blockedTags"`
		} `json:"me"`
	}
	if err := c.Do(ctx, queryBlockedTags, nil, &out); err != nil {
		return nil, err
	}
	if out.Me == nil {
		return nil, nil
	}
	names := make([]string, len(out.Me.BlockedTags))
	for i, t := range out.Me.BlockedTags {
		names[i] = t.Name
	}
	return names, nil
}

// UserByName looks up a single user by exact username. Returns nil if not found.
// (The API does NOT support user autocomplete — only exact match.)
func (c *Client) UserByName(ctx context.Context, name string) (*UserInfo, error) {
	var out struct {
		User *UserInfo `json:"user"`
	}
	if err := c.Do(ctx, queryUserByName, map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	return out.User, nil
}

// TagAutocomplete returns up to ~15 tag suggestions matching the given mask,
// sorted by post count descending (ties broken by name) so the most popular
// tags surface first — the API itself doesn't guarantee any particular order.
// Both English and Cyrillic input work; mask must be non-empty.
func (c *Client) TagAutocomplete(ctx context.Context, mask string) ([]TagSuggestion, error) {
	var out struct {
		TagAutocomplete []TagSuggestion `json:"tagAutocomplete"`
	}
	if err := c.Do(ctx, queryTagAutocomplete, map[string]any{"mask": mask}, &out); err != nil {
		return nil, err
	}
	sort.SliceStable(out.TagAutocomplete, func(i, j int) bool {
		a, b := out.TagAutocomplete[i], out.TagAutocomplete[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Name < b.Name
	})
	return out.TagAutocomplete, nil
}

// TagPosts fetches one page of posts for a tag, filtered by line type (ALL/NEW/GOOD/BEST).
func (c *Client) TagPosts(ctx context.Context, name string, line PostLineType, page int) (PostPager, error) {
	var out struct {
		Tag *struct {
			PostPager PostPager `json:"postPager"`
		} `json:"tag"`
	}
	vars := map[string]any{"name": name, "type": string(line), "page": page}
	if err := c.Do(ctx, queryTagPosts, vars, &out); err != nil {
		return PostPager{}, err
	}
	if out.Tag == nil {
		return PostPager{}, ErrTagNotFound
	}
	return out.Tag.PostPager, nil
}

// UserPosts fetches one page of posts authored by a user.
func (c *Client) UserPosts(ctx context.Context, username string, page int) (PostPager, error) {
	var out struct {
		User *struct {
			PostPager PostPager `json:"postPager"`
		} `json:"user"`
	}
	vars := map[string]any{"username": username, "page": page}
	if err := c.Do(ctx, queryUserPosts, vars, &out); err != nil {
		return PostPager{}, err
	}
	if out.User == nil {
		return PostPager{}, ErrUserNotFound
	}
	return out.User.PostPager, nil
}

// Search runs Query.search and returns its SearchResult for the requested page.
func (c *Client) Search(ctx context.Context, p SearchParams) (SearchResult, error) {
	vars := map[string]any{
		"query":               p.Query,
		"tagNames":            p.TagNames, // nil slice ⇒ null
		"username":            nilIfEmpty(p.Username),
		"showNsfw":            p.ShowNsfw,
		"showOnlyNsfw":        p.ShowOnlyNsfw,
		"showUnsafe":          p.ShowUnsafe,
		"sortByDate":          p.SortByDate,
		"sortByRating":        p.SortByRating,
		"minRating":           p.MinRating,
		"maxRating":           p.MaxRating,
		"searchInMyFavorites": p.SearchInMyFavorites,
		"page":                p.Page,
	}
	var out struct {
		Search SearchResult `json:"search"`
	}
	if err := c.Do(ctx, querySearch, vars, &out); err != nil {
		return SearchResult{}, err
	}
	return out.Search, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
