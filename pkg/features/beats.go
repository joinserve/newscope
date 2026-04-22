package features

import (
	"strings"

	"github.com/umputun/newscope/pkg/config"
)

// BeatsEnabled returns whether beat aggregation is active.
// Callers must guard any beats-related I/O with this check.
func BeatsEnabled(cfg config.Config) bool {
	return strings.TrimSpace(cfg.Embedding.Provider) != ""
}
