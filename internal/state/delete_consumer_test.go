package state

import "testing"

func TestExtractKVKeyFromSubject(t *testing.T) {
	t.Parallel()

	key := extractKVKeyFromSubject("tick", "$KV.tick.rule/a/b")
	if key != "rule/a/b" {
		t.Fatalf("unexpected key %q", key)
	}
	if out := extractKVKeyFromSubject("tick", "$KV.other.rule/a/b"); out != "" {
		t.Fatalf("expected empty key, got %q", out)
	}
}
