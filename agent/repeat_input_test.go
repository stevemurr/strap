package agent

import "testing"

func TestRepeatBookkeepingDoesNotEraseNestedWork(t *testing.T) {
	ignored := map[string]bool{"expected_revision": true}
	a := []byte(`{"input":{"expected_revision":1,"payload":{"expected_revision":10,"value":"a"}}}`)
	bookkeeping := []byte(`{"input":{"payload":{"expected_revision":10,"value":"a"},"expected_revision":2}}`)
	nested := []byte(`{"input":{"expected_revision":2,"payload":{"expected_revision":11,"value":"a"}}}`)
	value := []byte(`{"input":{"expected_revision":2,"payload":{"expected_revision":10,"value":"b"}}}`)
	if normalizeArguments(a, ignored) != normalizeArguments(bookkeeping, ignored) {
		t.Fatal("bookkeeping created false progress")
	}
	for _, b := range [][]byte{nested, value} {
		if normalizeArguments(a, ignored) == normalizeArguments(b, ignored) {
			t.Fatal("substantive nested change erased")
		}
	}
}
