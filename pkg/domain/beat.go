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

// Beat holds the minimal data needed by merge_worker to produce canonical fields.
// Members are loaded by ListPendingMerge and passed directly to the Merger.
type Beat struct {
	ID      int64
	Members []ClassifiedItem
}

// BeatCanonical is the LLM-generated output that represents a beat as a whole.
// merge_worker stores it via BeatStore.SaveCanonical.
type BeatCanonical struct {
	Title   string
	Summary string
}
