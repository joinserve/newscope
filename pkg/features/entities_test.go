package features

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/newscope/pkg/config"
)

func TestEntitiesEnabled(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		provider string
		want     bool
	}{
		{"disabled flag", false, "openai", false},
		{"empty provider", true, "", false},
		{"whitespace-only provider", true, "  ", false},
		{"enabled with provider", true, "openai", true},
		{"provider with surrounding whitespace", true, " openai ", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg config.Config
			cfg.Entities.Enabled = tc.enabled
			cfg.Entities.Provider = tc.provider
			assert.Equal(t, tc.want, EntitiesEnabled(cfg))
		})
	}
}
