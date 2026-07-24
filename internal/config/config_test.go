package config

import "testing"

func TestSemanticEnabledDefault(t *testing.T) {
	// Omitted -> defaults to true (preserves the semantic tier for existing routes).
	if !(Cache{}).SemanticEnabled() {
		t.Fatal("omitted semantic should default to true")
	}
	// Explicitly disabled -> false (exact-hash-only, for answer-sensitive routes).
	off := false
	if (Cache{Semantic: &off}).SemanticEnabled() {
		t.Fatal("semantic: false should disable the semantic tier")
	}
	on := true
	if !(Cache{Semantic: &on}).SemanticEnabled() {
		t.Fatal("semantic: true should enable the semantic tier")
	}
}
