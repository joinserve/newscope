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

// TitleRevision is a snapshot of a beat's canonical title and summary at a point in time.
type TitleRevision struct {
	ID          int64
	BeatID      int64
	Title       string
	Summary     string
	GeneratedAt time.Time
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

// TimelineSegment groups a title revision with the members that arrived during its period.
type TimelineSegment struct {
	Revision  TitleRevision
	Members   []ClassifiedItem
	IsCurrent bool
}

// BeatTimeline holds the timeline segments for a beat's detail page.
type BeatTimeline struct {
	Segments []TimelineSegment // ordered newest first
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
	return b.PrimaryTopicWithCounts(nil)
}

// PrimaryTopicWithCounts returns the most frequent topic across all members.
// When multiple topics tie for the highest member-count, globalCounts breaks the tie
// by picking the one with the largest global count. Falls back to first occurrence
// when globalCounts is nil or the winning topic is not present.
func (b *BeatWithMembers) PrimaryTopicWithCounts(globalCounts map[string]int) string {
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

	// find the highest member-count
	maxCount := 0
	for _, t := range order {
		if counts[t] > maxCount {
			maxCount = counts[t]
		}
	}

	// collect all topics that tied at maxCount
	var tied []string
	for _, t := range order {
		if counts[t] == maxCount {
			tied = append(tied, t)
		}
	}

	if len(tied) == 1 || len(globalCounts) == 0 {
		return tied[0]
	}

	// break the tie using global counts
	best := tied[0]
	for _, t := range tied[1:] {
		if globalCounts[t] > globalCounts[best] {
			best = t
		}
	}
	return best
}
