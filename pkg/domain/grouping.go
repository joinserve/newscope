package domain

import "time"

// Grouping is a user-defined named stream that collects beats whose tag set
// contains all of the grouping's tags (subset match, first-match-wins by
// display_order). Tags are stored lowercase.
type Grouping struct {
	ID           int64
	Name         string
	Slug         string
	Tags         []string
	DisplayOrder int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
