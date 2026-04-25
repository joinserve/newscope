package features

import (
	"strings"

	"github.com/umputun/newscope/pkg/config"
)

// EntitiesEnabled returns whether entity extraction is active.
// Callers must guard any entity-extraction I/O with this check.
func EntitiesEnabled(cfg config.Config) bool {
	return cfg.Entities.Enabled && strings.TrimSpace(cfg.Entities.Provider) != ""
}
