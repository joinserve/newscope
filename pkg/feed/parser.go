package feed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/umputun/newscope/pkg/domain"
)

// RSSHubScheme is the custom URL scheme expanded against the configured RSSHub host.
const RSSHubScheme = "rsshub://"

// Parser parses RSS/Atom feeds
type Parser struct {
	client     *http.Client
	userAgent  string
	rsshubHost string
}

// NewParser creates a new feed parser. rsshubHost is the base URL used to expand
// rsshub:// feed URLs; may be empty if the rsshub:// scheme is not used.
func NewParser(timeout time.Duration, userAgent, rsshubHost string) *Parser {
	return &Parser{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		userAgent:  userAgent,
		rsshubHost: strings.TrimRight(rsshubHost, "/"),
	}
}

// resolveURL expands rsshub:// URLs against the configured RSSHub host.
// Returns the URL unchanged for any other scheme.
func (p *Parser) resolveURL(rawURL string) (string, error) {
	if !strings.HasPrefix(rawURL, RSSHubScheme) {
		return rawURL, nil
	}
	if p.rsshubHost == "" {
		return "", fmt.Errorf("rsshub:// scheme requires rsshub.host to be configured")
	}
	path := strings.TrimPrefix(rawURL, RSSHubScheme)
	return p.rsshubHost + "/" + strings.TrimLeft(path, "/"), nil
}

// Parse fetches and parses a feed from the given URL
func (p *Parser) Parse(ctx context.Context, url string) (*domain.ParsedFeed, error) {
	resolved, err := p.resolveURL(url)
	if err != nil {
		return nil, err
	}
	// fetch feed content
	body, err := p.fetch(ctx, resolved)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}
	defer body.Close()

	// parse feed
	parser := gofeed.NewParser()
	feed, err := parser.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	// convert to our types
	result := &domain.ParsedFeed{
		Title:       feed.Title,
		Description: feed.Description,
		Link:        feed.Link,
		Items:       make([]domain.ParsedItem, 0, len(feed.Items)),
	}

	for _, item := range feed.Items {
		parsedItem := domain.ParsedItem{
			Title:       item.Title,
			Link:        item.Link,
			Description: item.Description,
			Content:     item.Content,
		}

		// set GUID
		if item.GUID != "" {
			parsedItem.GUID = item.GUID
		} else if item.Link != "" {
			parsedItem.GUID = item.Link
		} else {
			parsedItem.GUID = fmt.Sprintf("%s-%s", feed.Title, item.Title)
		}

		// set author
		if item.Author != nil {
			parsedItem.Author = item.Author.Name
		}

		// set published time
		if item.PublishedParsed != nil {
			parsedItem.Published = *item.PublishedParsed
		} else if item.UpdatedParsed != nil {
			parsedItem.Published = *item.UpdatedParsed
		}

		result.Items = append(result.Items, parsedItem)
	}

	return result, nil
}

// fetch retrieves content from a URL
func (p *Parser) fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", p.userAgent)

	// add browser-like headers
	addBrowserHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch URL: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return resp.Body, nil
}
