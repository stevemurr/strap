package content

import "testing"

func TestCloneOwnsImageBytesAndLabels(t *testing.T) {
	source := Content{{Text: "page one"}, {Image: &Image{MIMEType: "image/png", Data: []byte{1, 2, 3}}}}
	clone := source.Clone()
	clone[0].Text = "changed"
	clone[1].Image.MIMEType = "image/jpeg"
	clone[1].Image.Data[0] = 9
	if source[0].Text != "page one" || source[1].Image.MIMEType != "image/png" || source[1].Image.Data[0] != 1 {
		t.Fatal("clone shares mutable content")
	}
}
