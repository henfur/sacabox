package main

import (
	"slices"
	"testing"
)

func TestFlatten(t *testing.T) {
	got := flatten(map[string]any{
		"colors": map[string]any{"primary": map[string]any{"background": "#2b1212"}},
		"font":   map[string]any{"size": 11.5},
		"window": map[string]any{"opacity": float64(1), "padding": map[string]any{"x": float64(4)}},
	}, nil)
	want := []string{
		`colors.primary.background="#2b1212"`,
		"font.size=11.5",
		"window.opacity=1",
		"window.padding.x=4",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
}
