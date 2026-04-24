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

// BeatView is the read-side representation of a beat, surfaced to HTTP handlers
// that don't need member data (search JSON, programmatic callers).
type BeatView struct {
	ID               int64
	CanonicalTitle   string
	CanonicalSummary string
	FirstSeenAt      time.Time
	LastViewedAt     *time.Time
	Feedback         string // "like", "dislike", or "" (no signal)
	FeedbackAt       *time.Time
	MemberCount      int
}

// BeatWithMembers is the UI-side representation of a beat with its member
// items eager-loaded, used by inbox cards and detail pages.
type BeatWithMembers struct {
	ID               int64
	CanonicalTitle   *string
	CanonicalSummary *string
	FirstSeenAt      time.Time
	LastViewedAt     *time.Time
	UnreadCount      int
	AggregateScore   float64
	UserFeedback     string // "like", "dislike", or "" — sourced from beats.feedback
	FeedbackAt       *time.Time
	Topics           []string // union of member topics
	Members          []ClassifiedItem
}

// GetUserFeedback returns user feedback as string or empty string.
func (b *BeatWithMembers) GetUserFeedback() string {
	return b.UserFeedback
}

// PrimaryTopic returns the topic that appears most frequently across all member items.
// Ties are broken by first occurrence. Returns "" when no member has any topic.
func (b *BeatWithMembers) PrimaryTopic() string {
	counts := make(map[string]int)
	var order []string

	for _, m := range b.Members {
		for _, t := range m.GetTopics() {
			if _, seen := counts[t]; !seen {
				order = append(order, t)
			}
			counts[t]++
		}
	}

	if len(order) == 0 {
		return ""
	}

	best := order[0]
	for _, t := range order[1:] {
		if counts[t] > counts[best] {
			best = t
		}
	}
	return best
}
