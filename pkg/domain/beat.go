package domain

import "time"

// BeatCandidate is a classified item with embedding awaiting beat assignment.
// Lives in domain so both the scheduler (consumer) and repository
// (implementation) can reference it without importing each other.
type BeatCandidate struct {
	ItemID      int64
	Vector      []float32
	PublishedAt time.Time
}
