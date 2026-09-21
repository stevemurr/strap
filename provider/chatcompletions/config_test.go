package chatcompletions_test

import (
	"github.com/stevemurr/strap/provider/chatcompletions"
	"testing"
)

func TestConfigurationErrors(t *testing.T) {
	for _, c := range []chatcompletions.Config{{BaseURL: "invalid", Model: "model"}, {BaseURL: "https://model.test", Model: " "}} {
		if _, err := chatcompletions.New(c); err == nil {
			t.Fatal("accepted", c)
		}
	}
	if got := (&chatcompletions.HTTPError{Adapter: "chatcompletions", StatusCode: 503, Body: "unavailable"}).Error(); got != "chatcompletions: HTTP 503: unavailable" {
		t.Fatal(got)
	}
}
