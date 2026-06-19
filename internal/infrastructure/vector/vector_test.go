package vector

import (
	"testing"
)

func TestCalculateKeywordDistance(t *testing.T) {
	tests := []struct {
		name             string
		text             string
		keywords         []string
		fallbackDistance float64
		verify           func(t *testing.T, distance float32)
	}{
		{
			name:             "empty keywords",
			text:             "some text here",
			keywords:         []string{},
			fallbackDistance: 0.5,
			verify: func(t *testing.T, distance float32) {
				if distance != 0.5 {
					t.Errorf("expected 0.5, got %f", distance)
				}
			},
		},
		{
			name:             "no matches",
			text:             "some text here",
			keywords:         []string{"missing"},
			fallbackDistance: 0.6,
			verify: func(t *testing.T, distance float32) {
				if distance != 0.6 {
					t.Errorf("expected 0.6, got %f", distance)
				}
			},
		},
		{
			name:             "single keyword match",
			text:             "hello world",
			keywords:         []string{"hello"},
			fallbackDistance: 0.5,
			verify: func(t *testing.T, distance float32) {
				// Should be less than 0.5 because it matches 1/1 keywords
				if distance >= 0.5 {
					t.Errorf("expected distance < 0.5, got %f", distance)
				}
			},
		},
		{
			name:             "more matching keywords is better",
			text:             "hello world and golang",
			keywords:         []string{"hello", "golang"},
			fallbackDistance: 0.5,
			verify: func(t *testing.T, distance float32) {
				dist1 := calculateKeywordDistance("hello world", []string{"hello", "golang"}, 0.5)
				dist2 := calculateKeywordDistance("hello world and golang", []string{"hello", "golang"}, 0.5)
				if dist2 >= dist1 {
					t.Errorf("expected matching more keywords to have smaller distance: %f vs %f", dist2, dist1)
				}
			},
		},
		{
			name:             "more keyword occurrences is better",
			text:             "golang golang golang golang",
			keywords:         []string{"golang"},
			fallbackDistance: 0.5,
			verify: func(t *testing.T, distance float32) {
				dist1 := calculateKeywordDistance("golang is ok", []string{"golang"}, 0.5)
				dist2 := calculateKeywordDistance("golang golang golang golang is better", []string{"golang"}, 0.5)
				if dist2 >= dist1 {
					t.Errorf("expected higher term frequency to have smaller distance: %f vs %f", dist2, dist1)
				}
			},
		},
		{
			name:             "respects fallback distance as upper bound",
			text:             "hello world",
			keywords:         []string{"hello"},
			fallbackDistance: 0.8,
			verify: func(t *testing.T, distance float32) {
				if distance >= 0.8 {
					t.Errorf("expected distance to be bounded by fallback distance 0.8, got %f", distance)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dist := calculateKeywordDistance(tt.text, tt.keywords, tt.fallbackDistance)
			tt.verify(t, dist)
		})
	}
}
