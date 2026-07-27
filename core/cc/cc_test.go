package cc

import (
	"testing"
	"time"
)

func TestInitialCWNDBeforeAnySample(t *testing.T) {
	c := NewController()
	if got := c.CWND(); got != initialCWND {
		t.Errorf("CWND() = %d, want %d", got, initialCWND)
	}
	if got := c.DeliveryRate(); got != 0 {
		t.Errorf("DeliveryRate() = %v, want 0", got)
	}
}

func TestSampleIgnoresInvalidInputs(t *testing.T) {
	c := NewController()
	c.Sample(1_000_000, 0, 10*time.Millisecond)  // elapsed <= 0
	c.Sample(1_000_000, 100*time.Millisecond, 0) // rtProp <= 0
	c.Sample(1_000_000, 100*time.Millisecond, -time.Millisecond)
	if got := c.CWND(); got != initialCWND {
		t.Errorf("CWND() after invalid samples = %d, want unchanged %d", got, initialCWND)
	}
}

func TestCWNDFollowsBandwidthDelayProduct(t *testing.T) {
	c := NewController()
	// 10 MB/s delivery rate, 20ms rt_prop -> btl_bw*rt_prop*gain = 10e6 * 0.02 * 2 = 400000.
	rtProp := 20 * time.Millisecond
	for i := 0; i < btlBwWindow; i++ {
		c.Sample(200_000, 20*time.Millisecond, rtProp) // 200KB / 20ms = 10MB/s
	}
	want := int64(400_000)
	got := c.CWND()
	if got != want {
		t.Errorf("CWND() = %d, want %d", got, want)
	}
	if dr := c.DeliveryRate(); dr < 9_900_000 || dr > 10_100_000 {
		t.Errorf("DeliveryRate() = %v, want ~10e6", dr)
	}
}

func TestCWNDFloorsAtMinimum(t *testing.T) {
	c := NewController()
	// Tiny delivery rate and tiny RTT should still floor at minCWND, not collapse to ~0.
	for i := 0; i < btlBwWindow; i++ {
		c.Sample(10, time.Second, time.Microsecond)
	}
	if got := c.CWND(); got != minCWND {
		t.Errorf("CWND() = %d, want floor %d", got, minCWND)
	}
}

func TestCWNDCapsAtMaximum(t *testing.T) {
	c := NewController()
	for i := 0; i < btlBwWindow; i++ {
		c.Sample(1_000_000_000, time.Millisecond, time.Second)
	}
	if got := c.CWND(); got != maxCWND {
		t.Errorf("CWND() = %d, want ceiling %d", got, maxCWND)
	}
}

func TestWindowedMaxFilterSurvivesOneBadSample(t *testing.T) {
	c := NewController()
	rtProp := 20 * time.Millisecond
	for i := 0; i < btlBwWindow; i++ {
		c.Sample(200_000, 20*time.Millisecond, rtProp) // 10MB/s
	}
	highCWND := c.CWND()

	c.Sample(0, 20*time.Millisecond, rtProp) // one stalled sample: 0 bytes delivered
	if got := c.CWND(); got != highCWND {
		t.Errorf("CWND() after one bad sample = %d, want unchanged %d (max-filter should absorb it)", got, highCWND)
	}
}

func TestStalledPathDrainsOutOfWindow(t *testing.T) {
	c := NewController()
	rtProp := 20 * time.Millisecond
	for i := 0; i < btlBwWindow; i++ {
		c.Sample(200_000, 20*time.Millisecond, rtProp) // 10MB/s
	}
	highCWND := c.CWND()

	// Enough consecutive zero samples to cycle the good ones out of the window entirely.
	for i := 0; i < btlBwWindow; i++ {
		c.Sample(0, 20*time.Millisecond, rtProp)
	}
	if got := c.CWND(); got >= highCWND {
		t.Errorf("CWND() after sustained stall = %d, want less than %d", got, highCWND)
	}
	if got := c.CWND(); got != minCWND {
		t.Errorf("CWND() after full window of zero samples = %d, want floor %d", got, minCWND)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := NewController()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			c.Sample(1000, time.Millisecond, time.Millisecond)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = c.CWND()
		_ = c.DeliveryRate()
	}
	<-done
}
