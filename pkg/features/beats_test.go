package features

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/newscope/pkg/config"
)

func TestBeatsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{"empty provider disables beats", "", false},
		{"whitespace-only provider disables beats", "  ", false},
		{"openai provider enables beats", "openai", true},
		{"gemini provider enables beats", "gemini", true},
		{"provider with surrounding whitespace enables beats", " openai ", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg config.Config
			cfg.Embedding.Provider = tc.provider
			assert.Equal(t, tc.want, BeatsEnabled(cfg))
		})
	}
}
