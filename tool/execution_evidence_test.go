package tool

import (
	"errors"
	"strings"
	"testing"
)

func TestExecutionReceiptBoundsEscapingAndRetainsCapture(t *testing.T) {
	original, _ := JSON(ShellResult{Output: strings.Repeat("\x00\"\\", 6000)})
	result, err := ExecutionResult(original, &ExecutionBinding{EvidenceRef: NewExecutionEvidenceRef()}, errors.New("failed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content.Text()) > ExecutionResponseBytes || !strings.Contains(result.Content.Text(), "read_evidence") || result.Captured.Text() != original.Content.Text() {
		t.Fatal("capture or receipt lost")
	}
}
