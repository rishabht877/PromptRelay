package router

import (
	"context"
	"errors"
	"testing"

	"github.com/rishabht877/PromptRelay/internal/config"
	"github.com/rishabht877/PromptRelay/internal/providers"
	"github.com/rishabht877/PromptRelay/internal/types"
)

type fakeProvider struct {
	name  string
	calls int
	fn    func() (types.ChatResponse, error)
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Chat(context.Context, string, types.ChatRequest) (types.ChatResponse, error) {
	f.calls++
	return f.fn()
}

func twoRoute() map[string]config.Route {
	return map[string]config.Route{
		"default": {Fallback: []config.Target{
			{Provider: "p1", Model: "m1"},
			{Provider: "p2", Model: "m2"},
		}},
	}
}

func TestDispatch_FirstSucceeds(t *testing.T) {
	p1 := &fakeProvider{name: "p1", fn: func() (types.ChatResponse, error) {
		return types.ChatResponse{ID: "a", Provider: "p1"}, nil
	}}
	p2 := &fakeProvider{name: "p2", fn: func() (types.ChatResponse, error) {
		t.Fatal("p2 should not be called when p1 succeeds")
		return types.ChatResponse{}, nil
	}}
	r := New(providers.Registry{"p1": p1, "p2": p2}, twoRoute())

	got, err := r.Dispatch(context.Background(), types.ChatRequest{Model: "default"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != "p1" || p1.calls != 1 || p2.calls != 0 {
		t.Fatalf("expected p1 only, got provider=%s p1.calls=%d p2.calls=%d", got.Provider, p1.calls, p2.calls)
	}
}

func TestDispatch_FallsOverOnRetryable(t *testing.T) {
	p1 := &fakeProvider{name: "p1", fn: func() (types.ChatResponse, error) {
		return types.ChatResponse{}, &types.ProviderError{Provider: "p1", StatusCode: 503}
	}}
	p2 := &fakeProvider{name: "p2", fn: func() (types.ChatResponse, error) {
		return types.ChatResponse{ID: "b", Provider: "p2"}, nil
	}}
	r := New(providers.Registry{"p1": p1, "p2": p2}, twoRoute())

	got, err := r.Dispatch(context.Background(), types.ChatRequest{Model: "default"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != "p2" || got.Model != "m2" {
		t.Fatalf("expected fallover to p2/m2, got %s/%s", got.Provider, got.Model)
	}
	if p1.calls != 1 || p2.calls != 1 {
		t.Fatalf("expected both called once, got p1=%d p2=%d", p1.calls, p2.calls)
	}
}

func TestDispatch_TerminalErrorStops(t *testing.T) {
	p1 := &fakeProvider{name: "p1", fn: func() (types.ChatResponse, error) {
		return types.ChatResponse{}, &types.ProviderError{Provider: "p1", StatusCode: 400, Message: "bad request"}
	}}
	p2 := &fakeProvider{name: "p2", fn: func() (types.ChatResponse, error) {
		t.Fatal("p2 must not be called after a terminal 4xx from p1")
		return types.ChatResponse{}, nil
	}}
	r := New(providers.Registry{"p1": p1, "p2": p2}, twoRoute())

	_, err := r.Dispatch(context.Background(), types.ChatRequest{Model: "default"})
	if err == nil {
		t.Fatal("expected terminal error")
	}
	if p2.calls != 0 {
		t.Fatalf("p2 should not have been called, calls=%d", p2.calls)
	}
}

func TestDispatch_AllFail(t *testing.T) {
	mk := func(name string) *fakeProvider {
		return &fakeProvider{name: name, fn: func() (types.ChatResponse, error) {
			return types.ChatResponse{}, &types.ProviderError{Provider: name, StatusCode: 500}
		}}
	}
	p1, p2 := mk("p1"), mk("p2")
	r := New(providers.Registry{"p1": p1, "p2": p2}, twoRoute())

	if _, err := r.Dispatch(context.Background(), types.ChatRequest{Model: "default"}); err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestDispatch_UnknownRoute(t *testing.T) {
	r := New(providers.Registry{}, twoRoute())
	_, err := r.Dispatch(context.Background(), types.ChatRequest{Model: "nope"})
	if !errors.Is(err, ErrUnknownRoute) {
		t.Fatalf("expected ErrUnknownRoute, got %v", err)
	}
}
