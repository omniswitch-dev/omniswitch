package accel

import (
	"errors"
	"sync"
	"testing"
)

func TestNewlinePatternsKeepRuleIndexes(t *testing.T) {
	requireAvailable(t)
	patterns := []string{"zzz\n", "qqq", "a\r\nb", "www"}
	rejected, err := ValidatePatterns(patterns)
	if err != nil {
		t.Fatalf("ValidatePatterns() error = %v", err)
	}
	if len(rejected) != 2 || rejected[0] != 0 || rejected[1] != 2 {
		t.Fatalf("rejected = %v, want [0 2] (patterns with line breaks)", rejected)
	}
	s, err := NewScanner(patterns)
	if err != nil {
		t.Fatalf("NewScanner() error = %v", err)
	}
	defer s.Close()
	matches, err := s.ScanFirst([]byte("qqq www zzz"))
	if err != nil {
		t.Fatalf("ScanFirst() error = %v", err)
	}
	got := map[int]bool{}
	for _, m := range matches {
		got[m.RuleIndex] = true
	}
	if !got[1] || !got[3] || got[0] || got[2] || len(got) != 2 {
		t.Fatalf("matched rule indexes = %v, want exactly {1, 3}", got)
	}
}

func TestSafePattern(t *testing.T) {
	for pattern, want := range map[string]bool{"abc": true, `a\nb`: true, "a\nb": false, "a\r": false, "": true} {
		if got := SafePattern(pattern); got != want {
			t.Errorf("SafePattern(%q) = %v, want %v", pattern, got, want)
		}
	}
}

func TestScannerCloseIsIdempotentAndStopsScans(t *testing.T) {
	requireAvailable(t)
	s, err := NewScanner(benchPatterns)
	if err != nil {
		t.Fatalf("NewScanner() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := s.Scan([]byte("bob@example.com")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Scan() after Close error = %v, want ErrUnavailable", err)
	}
	if s.created != 0 {
		t.Fatalf("instances still open after Close: %d", s.created)
	}
}

func TestScannerConcurrentScansAndClose(t *testing.T) {
	requireAvailable(t)
	s, err := NewScanner(benchPatterns)
	if err != nil {
		t.Fatalf("NewScanner() error = %v", err)
	}
	payload := []byte("contact bob@example.com, ssn 123-45-6789")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				matches, err := s.ScanFirst(payload)
				if err != nil {
					t.Errorf("ScanFirst() error = %v", err)
					return
				}
				if len(matches) != 2 {
					t.Errorf("matches = %+v, want email and ssn", matches)
					return
				}
			}
		}()
	}
	wg.Wait()
	if s.created < 1 || s.created > s.max {
		t.Fatalf("pool size = %d, want 1..%d", s.created, s.max)
	}

	// Close while scans are in flight: they either finish or report
	// ErrUnavailable, and every instance is released afterwards.
	start := make(chan struct{})
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 20; i++ {
				if _, err := s.Scan(payload); err != nil && !errors.Is(err, ErrUnavailable) {
					t.Errorf("Scan() error = %v", err)
					return
				}
			}
		}()
	}
	close(start)
	_ = s.Close()
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created != 0 {
		t.Fatalf("instances still open after Close and drain: %d", s.created)
	}
}

func BenchmarkAccelParallelScanFirst(b *testing.B) {
	if !Available() {
		b.Skip("accelerator module not embedded")
	}
	s, err := NewScanner(benchPatterns)
	if err != nil {
		b.Fatalf("NewScanner() error = %v", err)
	}
	defer s.Close()
	payload := []byte(repeat("lorem ipsum dolor sit amet ", 300) + "bob@example.com")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := s.ScanFirst(payload); err != nil {
				b.Fatal(err)
			}
		}
	})
}
