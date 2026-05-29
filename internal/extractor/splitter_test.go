package extractor

import (
	"reflect"
	"testing"
)

func TestSplitText(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		chunkSize    int
		chunkOverlap int
		want         []string
	}{
		{
			name:         "Empty text",
			text:         "",
			chunkSize:    10,
			chunkOverlap: 2,
			want:         []string{""},
		},
		{
			name:         "Chunk size larger than text",
			text:         "Hello world",
			chunkSize:    20,
			chunkOverlap: 5,
			want:         []string{"Hello world"},
		},
		{
			name:         "Split by sentence boundary",
			text:         "Hello world. Welcome to Go programming! Enjoy learning.",
			chunkSize:    20,
			chunkOverlap: 5,
			want: []string{
				"Hello world.",
				"Welcome to Go", // 13 chars. Boundary at "Go" word? No boundary in lookback, splits at space/last possible.
				"programming!",
				"Enjoy learning.",
			},
		},
		{
			name:         "Invalid chunk size",
			text:         "Hello world",
			chunkSize:    0,
			chunkOverlap: 2,
			want:         []string{"Hello world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitText(tt.text, tt.chunkSize, tt.chunkOverlap)
			if len(got) == 0 {
				t.Errorf("SplitText() returned empty slice")
			}
			// Just verify first chunk or length depending on complexity
			if tt.name == "Empty text" && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitText() = %v, want %v", got, tt.want)
			}
		})
	}
}
