package main

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendLabel_Dedup(t *testing.T) {
	l1 := Label{AID: 1, BID: 2, ShouldMerge: true}

	labels := appendLabel(nil, l1)
	require.Len(t, labels, 1)

	// same pair, same order
	labels = appendLabel(labels, l1)
	assert.Len(t, labels, 1)

	// same pair, reversed order
	labels = appendLabel(labels, Label{AID: 2, BID: 1, ShouldMerge: false})
	assert.Len(t, labels, 1)

	// different pair
	labels = appendLabel(labels, Label{AID: 1, BID: 3, ShouldMerge: true})
	assert.Len(t, labels, 2)
}

func TestSaveLoadLabels_RoundTrip(t *testing.T) {
	tmp, err := os.CreateTemp("", "labels-*.json")
	require.NoError(t, err)
	tmp.Close()
	defer os.Remove(tmp.Name())

	now := time.Now().UTC().Truncate(time.Second)
	src := []Label{
		{AID: 1, BID: 2, ShouldMerge: true, Similarity: 0.92, LabeledAt: now},
		{AID: 3, BID: 4, ShouldMerge: false, Similarity: 0.31, LabeledAt: now},
	}
	require.NoError(t, saveLabels(tmp.Name(), src))

	got, err := loadLabels(tmp.Name())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, src[0].AID, got[0].AID)
	assert.Equal(t, src[0].ShouldMerge, got[0].ShouldMerge)
	assert.InDelta(t, src[1].Similarity, got[1].Similarity, 1e-9)
}

func TestLoadLabels_Missing(t *testing.T) {
	labels, err := loadLabels("/nonexistent/does-not-exist.json")
	require.NoError(t, err)
	assert.Nil(t, labels)
}

func TestLoadLabels_Empty(t *testing.T) {
	tmp, err := os.CreateTemp("", "empty-*.json")
	require.NoError(t, err)
	tmp.Close()
	defer os.Remove(tmp.Name())

	labels, err := loadLabels(tmp.Name())
	require.NoError(t, err)
	assert.Nil(t, labels)
}

func TestBucketSample_Sampling(t *testing.T) {
	var pairs []candidate
	// 12 pairs in 0.8–0.9 bucket (should be capped at 10)
	for i := 0; i < 12; i++ {
		pairs = append(pairs, candidate{Similarity: 0.85})
	}
	// 5 pairs in 0.6–0.7 bucket (all included)
	for i := 0; i < 5; i++ {
		pairs = append(pairs, candidate{Similarity: 0.65})
	}
	// 25 low-sim pairs (capped at 20)
	for i := 0; i < 25; i++ {
		pairs = append(pairs, candidate{Similarity: 0.2})
	}

	result := bucketSample(pairs)

	var n85, n65, nLow int
	for _, p := range result {
		switch {
		case p.Similarity >= 0.8 && p.Similarity < 0.9:
			n85++
		case p.Similarity >= 0.6 && p.Similarity < 0.7:
			n65++
		case p.Similarity < 0.5:
			nLow++
		}
	}
	assert.LessOrEqual(t, n85, 10, "0.8–0.9 bucket capped at 10")
	assert.Equal(t, 5, n65, "0.6–0.7 bucket fully included")
	assert.LessOrEqual(t, nLow, 20, "low-sim sample capped at 20")
}

func TestBucketSample_Empty(t *testing.T) {
	assert.Empty(t, bucketSample(nil))
	assert.Empty(t, bucketSample([]candidate{}))
}

func TestBucketSample_HighSimOnly(t *testing.T) {
	pairs := []candidate{
		{Similarity: 0.95},
		{Similarity: 0.92},
		{Similarity: 0.91},
	}
	result := bucketSample(pairs)
	assert.Len(t, result, 3, "all 3 high-sim pairs included when under per-bucket cap")
}

func TestTrigramJaccard(t *testing.T) {
	tests := []struct {
		a, b    string
		wantGt  float64
		wantLte float64
	}{
		{"hello world", "hello world", 1.0, 1.0},
		{"golang is fast", "golang is fast", 1.0, 1.0},
		{"completely different", "nothing in common xyz", 0, 0.1},
		{"breaking news today", "breaking news today extra", 0.5, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			got := trigramJaccard(tt.a, tt.b)
			assert.GreaterOrEqual(t, got, tt.wantGt)
			assert.LessOrEqual(t, got, tt.wantLte)
		})
	}
}

func TestCosineSim(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	c := []float32{1, 0, 0}

	assert.InDelta(t, 0.0, cosineSim(a, b), 1e-6, "orthogonal vectors")
	assert.InDelta(t, 1.0, cosineSim(a, c), 1e-6, "identical vectors")
	assert.InDelta(t, 0.0, cosineSim([]float32{}, []float32{}), 1e-9)
	assert.InDelta(t, 0.0, cosineSim(a, []float32{1, 0}), 1e-9) // length mismatch
}
