//go:build !darwin && !linux

package agentbrowser

import "context"

func interruptBrowser(context.Context, string, string) (bool, error) { return false, nil }
