package content_test

import (
	"github.com/stevemurr/strap/content"
	"testing"
)

func TestTextAndImages(t *testing.T) {
	c := append(content.Text("first"), content.Part{Image: &content.Image{MIMEType: "image/png", Data: []byte{1}}}, content.Part{Text: "last"})
	if c.Text() != "first\nlast" || !c.HasImages() {
		t.Fatalf("mixed content: %q, images=%v", c.Text(), c.HasImages())
	}
	if content.Text("plain").HasImages() {
		t.Fatal("text has images")
	}
	if (content.Content(nil)).Text() != "" {
		t.Fatal("nil content has text")
	}
}

func TestValidateContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		c       content.Content
		invalid bool
	}{
		{"nil", nil, false}, {"text", content.Text("hello"), false},
		{"png", content.Content{{Image: &content.Image{MIMEType: "image/png", Data: []byte{1}}}}, false},
		{"jpeg", content.Content{{Image: &content.Image{MIMEType: "image/jpeg", Data: []byte{1}}}}, false},
		{"webp", content.Content{{Image: &content.Image{MIMEType: "image/webp", Data: []byte{1}}}}, false},
		{"mixed part", content.Content{{Text: "bad", Image: &content.Image{Data: []byte{1}}}}, true},
		{"empty image", content.Content{{Image: &content.Image{MIMEType: "image/png"}}}, true},
		{"unsupported", content.Content{{Image: &content.Image{MIMEType: "image/gif", Data: []byte{1}}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.c.Validate(); (err != nil) != tc.invalid {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}
