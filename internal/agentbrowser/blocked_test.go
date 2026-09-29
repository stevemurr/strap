package agentbrowser

import (
	"strings"
	"testing"
)

// The block pages are the ones the browser returned as content in a 14-page
// sample (2026-09-28); the others must still read.
func TestBlockedPages(t *testing.T) {
	article := strings.Repeat("Go 1.27 adds generic methods and struct literal keys. ", 100)
	for _, tc := range []struct {
		name               string
		title, text        string
		challenge, captcha bool
		blocked            bool
	}{
		{name: "Reddit's reCAPTCHA gate", title: "Reddit - Prove your humanity", text: "# Prove your humanity\n\nWe’re committed to safety and security. But not for bots. Complete the challenge below and let us know you’re a real person.\n\nReddit, Inc. © \"2026\". All rights reserved.", captcha: true, blocked: true},
		{name: "the gate's words alone", title: "Reddit - Prove your humanity", text: "# Prove your humanity", blocked: true},
		{name: "Cloudflare's block page", title: "Attention Required! | Cloudflare", text: "Please enable cookies.\n\n# Sorry, you have been blocked\n\n## You are unable to access medium.com", challenge: true, blocked: true},
		{name: "the block page's words alone", title: "medium.com", text: "Please enable cookies.\n\n# Sorry, you have been blocked", blocked: true},
		{name: "Cloudflare's challenge", title: "Just a moment...", text: "www.npmjs.com\n\nPerforming security verification", challenge: true, blocked: true},
		{name: "challenge markup on a long page", title: "Docs", text: article, challenge: true, blocked: true},
		{name: "an article with a login captcha", title: "Access denied: a history of 403", text: article, captcha: true},
		{name: "an article quoting a block page", title: "How Cloudflare blocks bots", text: article + "Sorry, you have been blocked."},
		{name: "a short page", title: "Example Domain", text: "This domain is for use in documentation examples without needing permission."},
		{name: "a short page saying access denied", title: "HTTP 403", text: "The server returns 403 Access Denied when the token lacks the scope."},
	} {
		if got := blockedPage(tc.title, tc.text, tc.challenge, tc.captcha); got != tc.blocked {
			t.Errorf("%s: blocked %v, want %v", tc.name, got, tc.blocked)
		}
	}
}
