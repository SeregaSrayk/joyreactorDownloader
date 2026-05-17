package graphql

import (
	"errors"
	"fmt"
	"strings"
)

// CDNHost is the default CDN host. img1..img15 mirror the same content.
const CDNHost = "img10.joyreactor.cc"

// FileURL returns the direct CDN URL for the picture attribute's file using
// the extension implied by attr.Image.Type. See FileURLForExt for the path
// rules and why the attribute id is the canonical name segment.
func (a Attribute) FileURL() (string, error) {
	if a.Image == nil {
		return "", errors.New("attribute has no image")
	}
	return a.FileURLForExt(strings.ToLower(string(a.Image.Type)))
}

// FileURLForExt builds the CDN URL with an explicit extension. Used to fetch
// JR's auto-transcoded .mp4 version of animated content (image.HasVideo=true),
// since the original .gif may not exist on disk for large/recent uploads.
//
// Storage path differs by attribute kind:
//
//	PostAttributePicture    → https://<host>/pics/post/full/-<numericId>.<ext>
//	CommentAttributePicture → https://<host>/pics/comment/-<numericId>.<ext>
//
// Filename uses the numeric attribute id (NOT the image id) — coincides on
// very old posts but diverges on new ones (verified on post 5153532).
func (a Attribute) FileURLForExt(ext string) (string, error) {
	if a.Type != AttrPicture {
		return "", fmt.Errorf("attribute %s is not a PICTURE", a.Type)
	}
	_, num, err := DecodeID(a.ID)
	if err != nil {
		return "", fmt.Errorf("attribute id %q: %w", a.ID, err)
	}
	pathSeg := "post/full"
	if a.Typename == "CommentAttributePicture" {
		pathSeg = "comment"
	}
	return fmt.Sprintf("https://%s/pics/%s/-%d.%s", CDNHost, pathSeg, num, ext), nil
}

// ThumbnailURL returns the small post-level thumbnail (~3KB jpg).
// Verified live: https://img2.joyreactor.cc/pics/thumbnail/post-<numericPostId>.jpg.
// Convenient for UI grids — uses the numeric Post id, not the picture's attribute id.
func (p Post) ThumbnailURL() (string, error) {
	_, num, err := DecodeID(p.ID)
	if err != nil {
		return "", fmt.Errorf("post id %q: %w", p.ID, err)
	}
	return fmt.Sprintf("https://%s/pics/thumbnail/post-%d.jpg", CDNHost, num), nil
}

// IsVideo reports whether the image is a video container (mp4/webm).
func (i Image) IsVideo() bool {
	return i.Type == ImageMP4 || i.Type == ImageWEBM
}

// IsAnimated reports whether the content is animated (gif or video container).
func (i Image) IsAnimated() bool {
	return i.HasVideo || i.Type == ImageGIF
}

// WebmURL returns the URL of JR's webm transcode of an animated picture.
// JR stores transcodes under /pics/post/webm/<tagSlug>-<attrId>.webm.
// tagSlug should be a tag's seoName (lowercased, url-safe). Returns "" if
// the slug is empty or the attribute is not a PostAttributePicture.
func (a Attribute) WebmURL(tagSlug string) (string, error) {
	if a.Type != AttrPicture {
		return "", fmt.Errorf("attribute %s is not a PICTURE", a.Type)
	}
	if a.Typename != "PostAttributePicture" {
		return "", errors.New("webm transcode is only available for post pictures")
	}
	if tagSlug == "" {
		return "", errors.New("empty tag slug")
	}
	_, num, err := DecodeID(a.ID)
	if err != nil {
		return "", fmt.Errorf("attribute id %q: %w", a.ID, err)
	}
	return fmt.Sprintf("https://%s/pics/post/webm/%s-%d.webm", CDNHost, tagSlug, num), nil
}
