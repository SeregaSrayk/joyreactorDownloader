package graphql

import "testing"

func TestEncodeDecodeID(t *testing.T) {
	cases := []struct {
		gid string
		typ string
		id  int64
	}{
		{"UG9zdEF0dHJpYnV0ZVBpY3R1cmU6NjQxNDI5MQ==", "PostAttributePicture", 6414291},
		{"VGFnOjg=", "Tag", 8},
		{"SW1hZ2U6MTMzMDI=", "Image", 13302},
		{"UG9zdEF0dHJpYnV0ZVBpY3R1cmU6NzMwNTU3Mg==", "PostAttributePicture", 7305572},
	}
	for _, c := range cases {
		typ, id, err := DecodeID(c.gid)
		if err != nil {
			t.Fatalf("DecodeID(%q): %v", c.gid, err)
		}
		if typ != c.typ || id != c.id {
			t.Errorf("DecodeID(%q) = (%q, %d), want (%q, %d)", c.gid, typ, id, c.typ, c.id)
		}
		if got := EncodeID(c.typ, c.id); got != c.gid {
			t.Errorf("EncodeID(%q, %d) = %q, want %q", c.typ, c.id, got, c.gid)
		}
	}
}

func TestAttribute_FileURL(t *testing.T) {
	// Verified live: post 5153532 picture is served from attribute id 7305572.
	a := Attribute{
		ID:    "UG9zdEF0dHJpYnV0ZVBpY3R1cmU6NzMwNTU3Mg==",
		Type:  AttrPicture,
		Image: &Image{ID: "SW1hZ2U6NTE0ODcxNzc=", Type: ImagePNG},
	}
	got, err := a.FileURL()
	if err != nil {
		t.Fatal(err)
	}
	want := "https://img10.joyreactor.cc/pics/post/full/-7305572.png"
	if got != want {
		t.Errorf("FileURL = %q, want %q", got, want)
	}

	// Non-picture must error.
	if _, err := (Attribute{Type: AttrYouTube}).FileURL(); err == nil {
		t.Error("expected error for YOUTUBE attribute")
	}
	// Missing image must error.
	if _, err := (Attribute{ID: "UG9zdEF0dHJpYnV0ZVBpY3R1cmU6MQ==", Type: AttrPicture}).FileURL(); err == nil {
		t.Error("expected error when Image is nil")
	}
}
