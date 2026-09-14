package tool

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
)

const ExecutionEvidencePrefix = "execution:"
const ExecutionResponseBytes = 16 * 1024

func NewExecutionEvidenceRef() string {
	var b [32]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic(err)
	}
	return ExecutionEvidencePrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// ExecutionResult retains the entire captured result in host evidence and bounds
// the serialized model receipt. Oversized captures are read through evidence pages.
func ExecutionResult(r Result, b *ExecutionBinding, cause error) (Result, error) {
	r.Execution = b
	r.Captured = r.Content.Clone()
	var raw json.RawMessage
	if json.Valid([]byte(r.Content.Text())) {
		raw = json.RawMessage(r.Content.Text())
	} else {
		raw, _ = json.Marshal(r.Content.Text())
	}
	receipt := struct {
		EvidenceRef  string          `json:"evidence_ref"`
		Result       json.RawMessage `json:"result,omitempty"`
		Error        string          `json:"error,omitempty"`
		ReadEvidence bool            `json:"read_evidence,omitempty"`
	}{EvidenceRef: b.EvidenceRef, Result: raw}
	if cause != nil {
		receipt.Error = cause.Error()
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return r, err
	}
	if len(encoded) > ExecutionResponseBytes {
		receipt.Result = nil
		receipt.ReadEvidence = true
		if len(receipt.Error) > 1024 {
			receipt.Error = receipt.Error[:1024]
		}
		encoded, err = json.Marshal(receipt)
	}
	r.Content = Text(string(encoded)).Content
	return r, err
}
