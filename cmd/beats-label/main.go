package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type minCfg struct {
	Database struct {
		DSN string `yaml:"dsn"`
	} `yaml:"database"`
}

type item struct {
	ID        int64
	FeedTitle string
	Published string
	Title     string
	Summary   string
	Vector    []float32
}

type candidate struct {
	A, B       item
	Similarity float64
}

// Label is one labeled pair in the golden fixture file.
type Label struct {
	AID         int64     `json:"a_id"`
	BID         int64     `json:"b_id"`
	ShouldMerge bool      `json:"should_merge"`
	Similarity  float64   `json:"similarity"`
	LabeledAt   time.Time `json:"labelled_at"` //nolint:misspell // spec-mandated field name
}

func main() {
	cfgPath := flag.String("config", "config.yml", "config file path")
	fixturePath := flag.String("fixture", "docs/fixtures/beats-golden.json", "output fixture path")
	window := flag.Duration("window", 48*time.Hour, "candidate time window")
	flag.Parse()

	if err := run(*cfgPath, *fixturePath, *window); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(cfgPath, fixturePath string, window time.Duration) error {
	data, err := os.ReadFile(cfgPath) //nolint:gosec // path comes from CLI flag
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var cfg minCfg
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if cfg.Database.DSN == "" {
		cfg.Database.DSN = "file:newscope.db?cache=shared&mode=rwc"
	}

	db, err := sqlx.Open("sqlite", cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	candidates, err := fetchCandidates(db, window)
	if err != nil {
		return fmt.Errorf("fetch candidates: %w", err)
	}
	if len(candidates) == 0 {
		fmt.Println("no candidates found in window")
		return nil
	}
	return runUI(candidates, fixturePath)
}

func fetchCandidates(db *sqlx.DB, window time.Duration) ([]candidate, error) {
	windowStr := fmt.Sprintf("-%d seconds", int(window.Seconds()))
	rows, err := db.Queryx(`
		SELECT i.id, f.title AS feed_title, i.published, i.title,
		       COALESCE(i.summary, '') AS summary, ie.vector AS vector
		FROM items i
		JOIN feeds f ON f.id = i.feed_id
		LEFT JOIN item_embeddings ie ON ie.item_id = i.id
		WHERE i.classified_at IS NOT NULL
		  AND i.published > datetime('now', ?)
		ORDER BY i.published DESC
		LIMIT 200`, windowStr)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer rows.Close()

	type row struct {
		ID        int64  `db:"id"`
		FeedTitle string `db:"feed_title"`
		Published string `db:"published"`
		Title     string `db:"title"`
		Summary   string `db:"summary"`
		Vector    []byte `db:"vector"`
	}

	var items []item
	for rows.Next() {
		var r row
		if err := rows.StructScan(&r); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		it := item{ID: r.ID, FeedTitle: r.FeedTitle, Published: r.Published, Title: r.Title, Summary: r.Summary}
		if len(r.Vector) > 0 {
			it.Vector = decodeBlob(r.Vector)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return bucketSample(buildPairs(items)), nil
}

func buildPairs(items []item) []candidate {
	useEmbed := false
	for _, it := range items {
		if it.Vector != nil {
			useEmbed = true
			break
		}
	}
	var pairs []candidate
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			var sim float64
			if useEmbed && a.Vector != nil && b.Vector != nil {
				sim = cosineSim(a.Vector, b.Vector)
			} else {
				sim = trigramJaccard(a.Title, b.Title)
			}
			pairs = append(pairs, candidate{A: a, B: b, Similarity: sim})
		}
	}
	return pairs
}

// bucketSample clusters pairs into 0.1-wide similarity buckets (0.5–1.0),
// samples up to 10 from each, and adds up to 20 random low-similarity pairs.
func bucketSample(pairs []candidate) []candidate {
	buckets := make(map[int][]candidate)
	var low []candidate
	for _, p := range pairs {
		if p.Similarity >= 0.5 {
			b := int(p.Similarity * 10)
			if b > 9 {
				b = 9
			}
			buckets[b] = append(buckets[b], p)
		} else {
			low = append(low, p)
		}
	}
	var result []candidate
	for b := 5; b <= 9; b++ {
		ps := buckets[b]
		rand.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
		if len(ps) > 10 {
			ps = ps[:10]
		}
		result = append(result, ps...)
	}
	rand.Shuffle(len(low), func(i, j int) { low[i], low[j] = low[j], low[i] })
	if len(low) > 20 {
		low = low[:20]
	}
	result = append(result, low...)
	rand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
	return result
}

func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func trigramJaccard(a, b string) float64 {
	ag := trigrams(strings.ToLower(a))
	bg := trigrams(strings.ToLower(b))
	if len(ag) == 0 && len(bg) == 0 {
		return 0
	}
	inter := 0
	for g := range ag {
		if bg[g] {
			inter++
		}
	}
	union := len(ag) + len(bg) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func trigrams(s string) map[string]bool {
	m := make(map[string]bool)
	r := []rune(s)
	for i := 0; i+2 < len(r); i++ {
		m[string(r[i:i+3])] = true
	}
	return m
}

func decodeBlob(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func runUI(candidates []candidate, fixturePath string) error {
	labels, err := loadLabels(fixturePath)
	if err != nil {
		return err
	}

	seen := make(map[[2]int64]bool)
	for _, l := range labels {
		a, b := l.AID, l.BID
		if a > b {
			a, b = b, a
		}
		seen[[2]int64{a, b}] = true
	}

	var todo []candidate
	for _, c := range candidates {
		a, b := c.A.ID, c.B.ID
		if a > b {
			a, b = b, a
		}
		if !seen[[2]int64{a, b}] {
			todo = append(todo, c)
		}
	}

	total := len(todo)
	fmt.Printf("found %d new candidates (%d already labeled)\n", total, len(candidates)-total)

	trunc := func(s string) string {
		if len(s) > 200 {
			return s[:200] + "..."
		}
		return s
	}
	scanner := bufio.NewScanner(os.Stdin)
	for i, c := range todo {
		fmt.Printf("\n--- %d/%d  similarity: %.3f ---\n", i+1, total, c.Similarity)
		fmt.Printf("[A] %-20s  %s\n    %s\n    %s\n", c.A.FeedTitle, c.A.Published, c.A.Title, trunc(c.A.Summary))
		fmt.Printf("[B] %-20s  %s\n    %s\n    %s\n", c.B.FeedTitle, c.B.Published, c.B.Title, trunc(c.B.Summary))
		fmt.Print("(y)merge (n)not-merge (s)kip (q)uit > ")

		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch line[0] {
		case 'y', 'Y':
			labels = appendLabel(labels, Label{AID: c.A.ID, BID: c.B.ID, ShouldMerge: true, Similarity: c.Similarity, LabeledAt: time.Now()})
			if err := saveLabels(fixturePath, labels); err != nil {
				return err
			}
		case 'n', 'N':
			labels = appendLabel(labels, Label{AID: c.A.ID, BID: c.B.ID, ShouldMerge: false, Similarity: c.Similarity, LabeledAt: time.Now()})
			if err := saveLabels(fixturePath, labels); err != nil {
				return err
			}
		case 's', 'S':
			// skipped
		case 'q', 'Q':
			fmt.Printf("quit. %d labeled total.\n", len(labels))
			return nil
		}
	}

	fmt.Printf("done. %d labeled total.\n", len(labels))
	return nil
}

func loadLabels(path string) ([]Label, error) {
	data, err := os.ReadFile(path) //nolint:gosec // user-specified fixture path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read fixture: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var labels []Label
	if err := json.Unmarshal(data, &labels); err != nil {
		return nil, fmt.Errorf("parse fixture: %w", err)
	}
	return labels, nil
}

func saveLabels(path string, labels []Label) error {
	data, err := json.MarshalIndent(labels, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // user-specified output path
		return fmt.Errorf("write fixture: %w", err)
	}
	return nil
}

// appendLabel adds l to existing only if the pair (a_id, b_id) is not already present.
func appendLabel(existing []Label, l Label) []Label {
	a, b := l.AID, l.BID
	if a > b {
		a, b = b, a
	}
	for _, e := range existing {
		ea, eb := e.AID, e.BID
		if ea > eb {
			ea, eb = eb, ea
		}
		if ea == a && eb == b {
			return existing
		}
	}
	return append(existing, l)
}
