package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/pkg/repository"
)

//go:generate moq -out mocks/feed_repo.go -pkg mocks -skip-ensure -fmt goimports . FeedRepo
//go:generate moq -out mocks/item_repo.go -pkg mocks -skip-ensure -fmt goimports . ItemRepo
//go:generate moq -out mocks/classification_repo.go -pkg mocks -skip-ensure -fmt goimports . ClassificationRepo
//go:generate moq -out mocks/setting_repo.go -pkg mocks -skip-ensure -fmt goimports . SettingRepo
//go:generate moq -out mocks/beat_repo.go -pkg mocks -skip-ensure -fmt goimports . BeatRepo
//go:generate moq -out mocks/grouping_repo.go -pkg mocks -skip-ensure -fmt goimports . GroupingRepo

// RepositoryAdapter adapts repositories to server.Database interface
type RepositoryAdapter struct {
	feedRepo           FeedRepo
	itemRepo           ItemRepo
	classificationRepo ClassificationRepo
	settingRepo        SettingRepo
	beatRepo           BeatRepo
	groupingRepo       GroupingRepo
}

// FeedRepo defines the feed repository interface used by the adapter
type FeedRepo interface {
	GetFeeds(ctx context.Context, enabledOnly bool) ([]domain.Feed, error)
	CreateFeed(ctx context.Context, feed *domain.Feed) error
	UpdateFeed(ctx context.Context, feedID int64, title, feedURL, iconURL string, fetchInterval time.Duration) error
	UpdateFeedStatus(ctx context.Context, feedID int64, enabled bool) error
	DeleteFeed(ctx context.Context, feedID int64) error
	GetActiveFeedNames(ctx context.Context, minScore float64) ([]string, error)
}

// ItemRepo defines the item repository interface used by the adapter
type ItemRepo interface {
	GetItems(ctx context.Context, limit int, minScore float64) ([]domain.Item, error)
}

// ClassificationRepo defines the classification repository interface used by the adapter
type ClassificationRepo interface {
	GetClassifiedItems(ctx context.Context, filter *domain.ItemFilter) ([]*domain.ClassifiedItem, error)
	GetClassifiedItemsCount(ctx context.Context, filter *domain.ItemFilter) (int, error)
	GetClassifiedItem(ctx context.Context, itemID int64) (*domain.ClassifiedItem, error)
	UpdateItemFeedback(ctx context.Context, itemID int64, feedback *domain.Feedback) error
	GetTopics(ctx context.Context) ([]string, error)
	GetTopicsFiltered(ctx context.Context, minScore float64) ([]string, error)
	GetTopTopicsByScore(ctx context.Context, minScore float64, limit int) ([]repository.TopicWithScore, error)
	GetFeedbackCount(ctx context.Context) (int64, error)
	SearchItems(ctx context.Context, searchQuery string, filter *domain.ItemFilter) ([]*domain.ClassifiedItem, error)
	GetSearchItemsCount(ctx context.Context, searchQuery string, filter *domain.ItemFilter) (int, error)
	GetBigTags(ctx context.Context, threshold int) (map[string]int, error)
}

// SettingRepo defines the setting repository interface used by the adapter
type SettingRepo interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// BeatRepo defines the beat repository interface used by the adapter.
// Search intentionally omitted here: master's BeatRepository.Search returns
// []domain.BeatView (no members) via FTS5; the UI needs BeatWithMembers.
// The UI implementer decides the final shape — either a SearchWithMembers
// method on the repo, or enrichment in the handler.
type BeatRepo interface {
	ListBeats(ctx context.Context, topic string, limit, offset int) ([]domain.BeatWithMembers, error)
	GetBeat(ctx context.Context, beatID int64) (domain.BeatWithMembers, error)
	MarkViewed(ctx context.Context, beatID int64) error
	SetFeedback(ctx context.Context, beatID int64, feedback string) error
	SearchWithMembers(ctx context.Context, query string, limit int) ([]domain.BeatWithMembers, error)
}

// GroupingRepo defines the grouping repository interface used by the adapter.
type GroupingRepo interface {
	ListGroupings(ctx context.Context) ([]domain.Grouping, error)
	GetGrouping(ctx context.Context, id int64) (domain.Grouping, error)
	GetGroupingBySlug(ctx context.Context, slug string) (domain.Grouping, error)
	CreateGrouping(ctx context.Context, g domain.Grouping) (int64, error)
	UpdateGrouping(ctx context.Context, g domain.Grouping) error
	DeleteGrouping(ctx context.Context, id int64) error
	ReorderGroupings(ctx context.Context, idsInOrder []int64) error
}

// NewRepositoryAdapter creates a new repository adapter from concrete repositories
func NewRepositoryAdapter(repos *repository.Repositories) *RepositoryAdapter {
	return &RepositoryAdapter{
		feedRepo:           repos.Feed,
		itemRepo:           repos.Item,
		classificationRepo: repos.Classification,
		settingRepo:        repos.Setting,
		beatRepo:           repos.Beat,
		groupingRepo:       repos.Grouping,
	}
}

// NewRepositoryAdapterWithInterfaces creates a new repository adapter with interface dependencies for testing.
// groupingRepo may be nil; grouping methods will return an error if called on a nil repo.
func NewRepositoryAdapterWithInterfaces(feedRepo FeedRepo, itemRepo ItemRepo, classificationRepo ClassificationRepo, settingRepo SettingRepo, beatRepo BeatRepo) *RepositoryAdapter {
	return &RepositoryAdapter{
		feedRepo:           feedRepo,
		itemRepo:           itemRepo,
		classificationRepo: classificationRepo,
		settingRepo:        settingRepo,
		beatRepo:           beatRepo,
	}
}

// GetFeeds returns all feeds from repository
func (r *RepositoryAdapter) GetFeeds(ctx context.Context) ([]domain.Feed, error) {
	feeds, err := r.feedRepo.GetFeeds(ctx, false) // get all feeds
	if err != nil {
		return nil, err
	}

	return feeds, nil
}

// GetItems returns items from repository
func (r *RepositoryAdapter) GetItems(ctx context.Context, limit, _ int) ([]domain.Item, error) {
	// repository uses minScore instead of offset
	// for now, return all items with score >= 0
	items, err := r.itemRepo.GetItems(ctx, limit, 0)
	if err != nil {
		return nil, err
	}

	return items, nil
}

// GetClassifiedItems returns items with classification data
func (r *RepositoryAdapter) GetClassifiedItems(ctx context.Context, minScore float64, topic string, limit int) ([]domain.ClassifiedItem, error) {
	req := domain.ArticlesRequest{
		MinScore: minScore,
		Topic:    topic,
		FeedName: "",
		SortBy:   "published",
		Limit:    limit,
	}
	return r.GetClassifiedItemsWithFilters(ctx, req)
}

// GetClassifiedItemsWithFilters returns items with classification data filtered by topic and feed
func (r *RepositoryAdapter) GetClassifiedItemsWithFilters(ctx context.Context, req domain.ArticlesRequest) ([]domain.ClassifiedItem, error) {
	// calculate offset from page number
	offset := 0
	if req.Page > 1 {
		offset = (req.Page - 1) * req.Limit
	}

	filter := &domain.ItemFilter{
		MinScore:      req.MinScore,
		Topic:         req.Topic,
		FeedName:      req.FeedName,
		SortBy:        req.SortBy,
		Limit:         req.Limit,
		Offset:        offset,
		ShowLikedOnly: req.ShowLikedOnly,
		ShowProcessed: req.ShowProcessed,
		DateFrom:      req.DateFrom,
	}

	// get items from repository
	items, err := r.classificationRepo.GetClassifiedItems(ctx, filter)
	if err != nil {
		return nil, err
	}

	// convert to domain.ClassifiedItem and handle feed name
	result := make([]domain.ClassifiedItem, 0, len(items))
	for _, item := range items {
		classified := *item
		classified.FeedName = getFeedDisplayName(item.FeedName, item.FeedURL)
		result = append(result, classified)
	}

	return result, nil
}

// GetClassifiedItemsCount returns total count of classified items matching filters
func (r *RepositoryAdapter) GetClassifiedItemsCount(ctx context.Context, req domain.ArticlesRequest) (int, error) {
	filter := &domain.ItemFilter{
		MinScore:      req.MinScore,
		Topic:         req.Topic,
		FeedName:      req.FeedName,
		SortBy:        req.SortBy,
		Limit:         req.Limit,
		ShowLikedOnly: req.ShowLikedOnly,
		ShowProcessed: req.ShowProcessed,
		DateFrom:      req.DateFrom,
	}

	return r.classificationRepo.GetClassifiedItemsCount(ctx, filter)
}

// UpdateItemFeedback updates user feedback for an item
func (r *RepositoryAdapter) UpdateItemFeedback(ctx context.Context, itemID int64, feedback string) error {
	feedbackType := domain.FeedbackType(feedback)
	domainFeedback := &domain.Feedback{
		Type: feedbackType,
	}
	return r.classificationRepo.UpdateItemFeedback(ctx, itemID, domainFeedback)
}

// GetClassifiedItem returns a single item with classification data
func (r *RepositoryAdapter) GetClassifiedItem(ctx context.Context, itemID int64) (*domain.ClassifiedItem, error) {
	item, err := r.classificationRepo.GetClassifiedItem(ctx, itemID)
	if err != nil {
		return nil, err
	}

	// handle feed name
	item.FeedName = getFeedDisplayName(item.FeedName, item.FeedURL)
	return item, nil
}

// GetTopics returns all unique topics from classified items
func (r *RepositoryAdapter) GetTopics(ctx context.Context) ([]string, error) {
	return r.classificationRepo.GetTopics(ctx)
}

// GetTopicsFiltered returns unique topics from items with score >= minScore
func (r *RepositoryAdapter) GetTopicsFiltered(ctx context.Context, minScore float64) ([]string, error) {
	return r.classificationRepo.GetTopicsFiltered(ctx, minScore)
}

// GetTopTopicsByScore returns topics ordered by average relevance score
func (r *RepositoryAdapter) GetTopTopicsByScore(ctx context.Context, minScore float64, limit int) ([]domain.TopicWithScore, error) {
	repoTopics, err := r.classificationRepo.GetTopTopicsByScore(ctx, minScore, limit)
	if err != nil {
		return nil, err
	}

	// convert repository type to domain type
	result := make([]domain.TopicWithScore, len(repoTopics))
	for i, topic := range repoTopics {
		result[i] = domain.TopicWithScore{
			Topic:     topic.Topic,
			AvgScore:  topic.AvgScore,
			ItemCount: topic.ItemCount,
		}
	}
	return result, nil
}

// GetFeedbackCount returns the total number of feedback items
func (r *RepositoryAdapter) GetFeedbackCount(ctx context.Context) (int64, error) {
	return r.classificationRepo.GetFeedbackCount(ctx)
}

// GetAllFeeds returns all feeds with full details
func (r *RepositoryAdapter) GetAllFeeds(ctx context.Context) ([]domain.Feed, error) {
	domainFeeds, err := r.feedRepo.GetFeeds(ctx, false) // get all feeds, not just enabled
	if err != nil {
		return nil, err
	}

	return domainFeeds, nil
}

// CreateFeed adds a new feed
func (r *RepositoryAdapter) CreateFeed(ctx context.Context, feed *domain.Feed) error {
	return r.feedRepo.CreateFeed(ctx, feed)
}

// UpdateFeed updates feed title and interval
func (r *RepositoryAdapter) UpdateFeed(ctx context.Context, feedID int64, title, feedURL, iconURL string, fetchInterval time.Duration) error {
	return r.feedRepo.UpdateFeed(ctx, feedID, title, feedURL, iconURL, fetchInterval)
}

// UpdateFeedStatus enables or disables a feed
func (r *RepositoryAdapter) UpdateFeedStatus(ctx context.Context, feedID int64, enabled bool) error {
	return r.feedRepo.UpdateFeedStatus(ctx, feedID, enabled)
}

// DeleteFeed removes a feed
func (r *RepositoryAdapter) DeleteFeed(ctx context.Context, feedID int64) error {
	return r.feedRepo.DeleteFeed(ctx, feedID)
}

// GetActiveFeedNames returns names of feeds that have classified articles
func (r *RepositoryAdapter) GetActiveFeedNames(ctx context.Context, minScore float64) ([]string, error) {
	return r.feedRepo.GetActiveFeedNames(ctx, minScore)
}

// GetSetting retrieves a setting value by key
func (r *RepositoryAdapter) GetSetting(ctx context.Context, key string) (string, error) {
	return r.settingRepo.GetSetting(ctx, key)
}

// SetSetting stores a setting value
func (r *RepositoryAdapter) SetSetting(ctx context.Context, key, value string) error {
	return r.settingRepo.SetSetting(ctx, key, value)
}

// SearchItems searches for items using full-text search
func (r *RepositoryAdapter) SearchItems(ctx context.Context, searchQuery string, req domain.ArticlesRequest) ([]domain.ClassifiedItem, error) {
	// calculate offset from page number
	offset := 0
	if req.Page > 1 {
		offset = (req.Page - 1) * req.Limit
	}

	filter := &domain.ItemFilter{
		MinScore:      req.MinScore,
		Topic:         req.Topic,
		FeedName:      req.FeedName,
		SortBy:        req.SortBy,
		Limit:         req.Limit,
		Offset:        offset,
		ShowLikedOnly: req.ShowLikedOnly,
		ShowProcessed: req.ShowProcessed,
		DateFrom:      req.DateFrom,
	}

	// get items from repository
	items, err := r.classificationRepo.SearchItems(ctx, searchQuery, filter)
	if err != nil {
		return nil, err
	}

	// convert to domain.ClassifiedItem and handle feed name
	result := make([]domain.ClassifiedItem, 0, len(items))
	for _, item := range items {
		classified := *item
		classified.FeedName = getFeedDisplayName(item.FeedName, item.FeedURL)
		result = append(result, classified)
	}

	return result, nil
}

// GetSearchItemsCount returns the total count of items matching the search query
func (r *RepositoryAdapter) GetSearchItemsCount(ctx context.Context, searchQuery string, req domain.ArticlesRequest) (int, error) {
	filter := &domain.ItemFilter{
		MinScore:      req.MinScore,
		Topic:         req.Topic,
		FeedName:      req.FeedName,
		SortBy:        req.SortBy,
		Limit:         req.Limit,
		ShowLikedOnly: req.ShowLikedOnly,
		ShowProcessed: req.ShowProcessed,
		DateFrom:      req.DateFrom,
	}

	return r.classificationRepo.GetSearchItemsCount(ctx, searchQuery, filter)
}

// GetBigTags returns tags that appear in at least threshold classified items.
func (r *RepositoryAdapter) GetBigTags(ctx context.Context, threshold int) (map[string]int, error) {
	return r.classificationRepo.GetBigTags(ctx, threshold)
}

// getFeedDisplayName returns the feed title if available, otherwise extracts hostname from URL
func getFeedDisplayName(title, feedURL string) string {
	if title != "" {
		return title
	}

	// try to extract hostname from URL
	if u, err := url.Parse(feedURL); err == nil && u.Hostname() != "" {
		host := u.Hostname()
		// remove www. prefix if present
		host = strings.TrimPrefix(host, "www.")
		return host
	}

	// fallback to the full URL
	return feedURL
}

// ListBeats lists beat aggregation summaries, optionally filtered by topic.
func (r *RepositoryAdapter) ListBeats(ctx context.Context, topic string, limit, offset int) ([]domain.BeatWithMembers, error) {
	if r.beatRepo == nil {
		return nil, nil // graceful degradation
	}
	return r.beatRepo.ListBeats(ctx, topic, limit, offset)
}

// SetFeedback updates the user feedback for a beat.
func (r *RepositoryAdapter) SetFeedback(ctx context.Context, beatID int64, feedback string) error {
	if r.beatRepo == nil {
		return nil // graceful degradation
	}
	return r.beatRepo.SetFeedback(ctx, beatID, feedback)
}

// GetBeat retrieves a single beat with its members
func (r *RepositoryAdapter) GetBeat(ctx context.Context, beatID int64) (domain.BeatWithMembers, error) {
	if r.beatRepo == nil {
		return domain.BeatWithMembers{}, fmt.Errorf("beats disabled")
	}
	return r.beatRepo.GetBeat(ctx, beatID)
}

// MarkViewed marks a beat as viewed
func (r *RepositoryAdapter) MarkViewed(ctx context.Context, beatID int64) error {
	if r.beatRepo == nil {
		return nil
	}
	return r.beatRepo.MarkViewed(ctx, beatID)
}

// SearchBeatsWithMembers searches for beats matching the query and loads their members
func (r *RepositoryAdapter) SearchBeatsWithMembers(ctx context.Context, query string, limit int) ([]domain.BeatWithMembers, error) {
	if r.beatRepo == nil {
		return nil, nil
	}
	return r.beatRepo.SearchWithMembers(ctx, query, limit)
}

// ListGroupings returns all user-defined groupings ordered by display_order.
func (r *RepositoryAdapter) ListGroupings(ctx context.Context) ([]domain.Grouping, error) {
	if r.groupingRepo == nil {
		return nil, fmt.Errorf("grouping repository not configured")
	}
	return r.groupingRepo.ListGroupings(ctx)
}

// GetGrouping returns a grouping by its id.
func (r *RepositoryAdapter) GetGrouping(ctx context.Context, id int64) (domain.Grouping, error) {
	if r.groupingRepo == nil {
		return domain.Grouping{}, fmt.Errorf("grouping repository not configured")
	}
	return r.groupingRepo.GetGrouping(ctx, id)
}

// GetGroupingBySlug returns a grouping by its URL slug.
func (r *RepositoryAdapter) GetGroupingBySlug(ctx context.Context, slug string) (domain.Grouping, error) {
	if r.groupingRepo == nil {
		return domain.Grouping{}, fmt.Errorf("grouping repository not configured")
	}
	return r.groupingRepo.GetGroupingBySlug(ctx, slug)
}

// CreateGrouping inserts a new grouping and returns its id.
func (r *RepositoryAdapter) CreateGrouping(ctx context.Context, g domain.Grouping) (int64, error) {
	if r.groupingRepo == nil {
		return 0, fmt.Errorf("grouping repository not configured")
	}
	return r.groupingRepo.CreateGrouping(ctx, g)
}

// UpdateGrouping updates name and tags of an existing grouping.
func (r *RepositoryAdapter) UpdateGrouping(ctx context.Context, g domain.Grouping) error {
	if r.groupingRepo == nil {
		return fmt.Errorf("grouping repository not configured")
	}
	return r.groupingRepo.UpdateGrouping(ctx, g)
}

// DeleteGrouping removes a grouping.
func (r *RepositoryAdapter) DeleteGrouping(ctx context.Context, id int64) error {
	if r.groupingRepo == nil {
		return fmt.Errorf("grouping repository not configured")
	}
	return r.groupingRepo.DeleteGrouping(ctx, id)
}

// ReorderGroupings sets display_order for the provided id list in order.
func (r *RepositoryAdapter) ReorderGroupings(ctx context.Context, idsInOrder []int64) error {
	if r.groupingRepo == nil {
		return fmt.Errorf("grouping repository not configured")
	}
	return r.groupingRepo.ReorderGroupings(ctx, idsInOrder)
}
