package claudecode

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, raw []byte, into any) {
	t.Helper()

	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
}
