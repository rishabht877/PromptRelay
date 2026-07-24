package cache

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/rishabht877/PromptRelay/internal/types"
)

func TestNormalizeCollapsesWhitespace(t *testing.T) {
	a := Normalize([]types.Message{{Role: "user", Content: "hello   world\n\ttrimmed"}})
	b := Normalize([]types.Message{{Role: "user", Content: "hello world trimmed"}})
	if a != b {
		t.Fatalf("whitespace should normalize equal:\n%q\n%q", a, b)
	}
}

func fp(f float64) *float64 { return &f }

func TestExactKeyStableAndSensitive(t *testing.T) {
	req1 := types.ChatRequest{Model: "default", Messages: []types.Message{{Role: "user", Content: "what is 2+2"}}}
	req2 := types.ChatRequest{Model: "default", Messages: []types.Message{{Role: "user", Content: "what is 2+3"}}}

	k1 := ExactKey(req1, Normalize(req1.Messages))
	k1again := ExactKey(req1, Normalize(req1.Messages))
	k2 := ExactKey(req2, Normalize(req2.Messages))

	if k1 != k1again {
		t.Fatal("ExactKey must be deterministic for identical input")
	}
	// The classic semantic-cache trap: near-identical prompts, different answers.
	// The exact key must distinguish them so they never collide on the fast path.
	if k1 == k2 {
		t.Fatal("'2+2' and '2+3' must produce different exact keys")
	}
}

func TestParamsAffectExactKey(t *testing.T) {
	base := types.ChatRequest{Model: "default", Messages: []types.Message{{Role: "user", Content: "hi"}}}
	warm := base
	warm.Temperature = fp(0.7)

	if ExactKey(base, Normalize(base.Messages)) == ExactKey(warm, Normalize(warm.Messages)) {
		t.Fatal("differing temperature must change the exact key")
	}
}

func TestEscapeTag(t *testing.T) {
	if got := escapeTag("gpt-4o-mini"); got != `gpt\-4o\-mini` {
		t.Fatalf("escapeTag = %q", got)
	}
	if got := escapeTag("llama3_2"); got != "llama3_2" {
		t.Fatalf("alphanumerics/underscore should be untouched, got %q", got)
	}
}

func TestFloat32sToBytesRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 3.5, 1e-3}
	b := float32sToBytes(in)
	if len(b) != 4*len(in) {
		t.Fatalf("expected %d bytes, got %d", 4*len(in), len(b))
	}
	for i, want := range in {
		got := math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		if got != want {
			t.Errorf("index %d: got %v want %v", i, got, want)
		}
	}
}
