package main

import (
	"testing"
	"time"
)

func TestSummarizeUsesNearestRankPercentiles(t *testing.T) {
	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Duration(100-i) * time.Millisecond
	}
	got := summarize(samples)
	if got.Count != 100 || got.MinMS != 1 || got.MedianMS != 50 || got.P95MS != 95 || got.MaxMS != 100 {
		t.Fatalf("summarize = %+v", got)
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 95); got != 0 {
		t.Fatalf("percentile(nil) = %v, want 0", got)
	}
}
