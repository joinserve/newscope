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

// BeatWithMembers represents a beat with all its member items eager-loaded.
type BeatWithMembers struct {
	ID               int64
	CanonicalTitle   *string
	CanonicalSummary *string
	FirstSeenAt      time.Time
	LastViewedAt     *time.Time
	UnreadCount      int
	AggregateScore   float64
	UserFeedback     string   // from settings table
	Topics           []string // union of member topics
	Members          []ClassifiedItem
}

// GetUserFeedback returns user feedback as string or empty string
func (b *BeatWithMembers) GetUserFeedback() string {
	return b.UserFeedback
}
