// Package content defines ordered text and image payloads shared by tools and providers.
package content

import (
	"errors"
	"strings"
)

type Image struct {
	MIMEType string
	Data     []byte
}

// A Part contains text or an image. Image data belongs to the content snapshot,
// not to a temporary file or a model-specific data URL.
type Part struct {
	Text  string
	Image *Image
}

type Content []Part

func Text(text string) Content { return Content{{Text: text}} }

func (c Content) Text() string {
	var parts []string
	for _, part := range c {
		if part.Image == nil {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}
func (c Content) HasImages() bool {
	for _, part := range c {
		if part.Image != nil {
			return true
		}
	}
	return false
}
func (c Content) Clone() Content {
	out := append(Content(nil), c...)
	for i := range out {
		if out[i].Image != nil {
			img := *out[i].Image
			img.Data = append([]byte(nil), img.Data...)
			out[i].Image = &img
		}
	}
	return out
}
func (c Content) Validate() error {
	for _, part := range c {
		if part.Image == nil {
			continue
		}
		if part.Text != "" {
			return errors.New("content part cannot contain both text and an image")
		}
		if len(part.Image.Data) == 0 {
			return errors.New("image content is empty")
		}
		switch part.Image.MIMEType {
		case "image/png", "image/jpeg", "image/webp":
		default:
			return errors.New("unsupported image MIME type")
		}
	}
	return nil
}
