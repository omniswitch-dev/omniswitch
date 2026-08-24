package accel

import (
	"regexp"
	"testing"
)

var benchPatterns = []string{
	`(?i)(ignore|disregard)\s+(all\s+)?(previous|prior|above)`,
	`sk-[a-zA-Z0-9]{32,}`,
	`AKIA[0-9A-Z]{16}`,
	`ghp_[a-zA-Z0-9]{36}`,
	`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`,
	`\b\d{3}-\d{2}-\d{4}\b`,
	`(?i)(DROP|DELETE|TRUNCATE)\s+(TABLE|DATABASE)`,
	`(?i)\bUNION\s+SELECT\b`,
}

func requireAvailable(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("accelerator module not embedded")
	}
}

func TestValidatePatterns(t *testing.T) {
	requireAvailable(t)
	rejected, err := ValidatePatterns([]string{`valid\d+`, `(?!invalid-lookahead)`, `also[\w]+`})
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if len(rejected) != 1 || rejected[0] != 1 {
		t.Fatalf("expected only index 1 rejected, got %v", rejected)
	}
}

func TestScanMatches(t *testing.T) {
	requireAvailable(t)
	s, err := NewScanner(benchPatterns)
	if err != nil {
		t.Fatalf("scanner init: %v", err)
	}
	defer s.Close()

	payload := []byte("contact bob@example.com or call 555-123-4567, key sk-" + repeat("a", 40))
	matches, err := s.Scan(payload)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(matches) < 2 {
		t.Fatalf("expected at least 2 matches (email + api key), got %d: %+v", len(matches), matches)
	}
	for _, m := range matches {
		if m.Start < 0 || m.End > len(payload) || m.Start >= m.End {
			t.Fatalf("match out of bounds: %+v", m)
		}
		switch m.RuleIndex {
		case 4: // email
			if got := string(payload[m.Start:m.End]); got != "bob@example.com" {
				t.Fatalf("email match wrong: %q", got)
			}
		case 1: // sk- key
			if got := string(payload[m.Start:m.End]); len(got) != 43 {
				t.Fatalf("key match wrong length %d: %q", len(got), got)
			}
		}
	}
}

func TestScanEmptyAndUnicode(t *testing.T) {
	requireAvailable(t)
	s, err := NewScanner([]string{"héllo", `world`})
	if err != nil {
		t.Fatalf("scanner init: %v", err)
	}
	defer s.Close()

	if m, err := s.Scan(nil); err != nil || len(m) != 0 {
		t.Fatalf("empty payload: %v %v", m, err)
	}
	matches, err := s.Scan([]byte("say héllo world"))
	if err != nil {
		t.Fatalf("unicode scan: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 unicode matches, got %d", len(matches))
	}
	if string([]byte("say héllo world")[matches[0].Start:matches[0].End]) != "héllo" {
		t.Fatalf("unicode offsets wrong: %+v", matches[0])
	}
}

func BenchmarkAccelScan8KB(b *testing.B) {
	if !Available() {
		b.Skip("accelerator module not embedded")
	}
	s, err := NewScanner(benchPatterns)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	payload := makePayload(8192)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Scan(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func makePayload(n int) []byte {
	base := []byte("User asks about invoice #12345. Contact finance@example.com for the totals. No secrets here. ")
	out := make([]byte, 0, n+len(base))
	for len(out)+len(base) <= n {
		out = append(out, base...)
	}
	return out
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for len(out) < n {
		out = append(out, s...)
	}
	return string(out[:n])
}

func BenchmarkGoRegexpScan8KB(b *testing.B) {
	compiled := make([]*regexp.Regexp, 0, len(benchPatterns))
	for _, p := range benchPatterns {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	payload := makePayload(8192)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, re := range compiled {
			re.Match(payload)
		}
	}
}

func BenchmarkAccelScan64KB(b *testing.B) {
	if !Available() {
		b.Skip("no accelerator")
	}
	s, _ := NewScanner(benchPatterns)
	defer s.Close()
	payload := makePayload(65536)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Scan(payload)
	}
}

func BenchmarkGoRegexpScan64KB(b *testing.B) {
	compiled := make([]*regexp.Regexp, 0, len(benchPatterns))
	for _, p := range benchPatterns {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	payload := makePayload(65536)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, re := range compiled {
			re.Match(payload)
		}
	}
}

func BenchmarkAccelScan256KB(b *testing.B) {
	if !Available() {
		b.Skip("no accelerator")
	}
	s, _ := NewScanner(benchPatterns)
	defer s.Close()
	payload := makePayload(262144)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Scan(payload)
	}
}

func BenchmarkGoRegexpScan256KB(b *testing.B) {
	compiled := make([]*regexp.Regexp, 0, len(benchPatterns))
	for _, p := range benchPatterns {
		compiled = append(compiled, regexp.MustCompile(p))
	}
	payload := makePayload(262144)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, re := range compiled {
			re.Match(payload)
		}
	}
}

func BenchmarkAccelFirst8KB(b *testing.B) {
	if !Available() {
		b.Skip("no accelerator")
	}
	s, _ := NewScanner(benchPatterns)
	defer s.Close()
	payload := makePayload(8192)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.ScanFirst(payload)
	}
}

func BenchmarkAccelFirst64KB(b *testing.B) {
	if !Available() {
		b.Skip("no accelerator")
	}
	s, _ := NewScanner(benchPatterns)
	defer s.Close()
	payload := makePayload(65536)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.ScanFirst(payload)
	}
}

func BenchmarkAccelFirst256KB(b *testing.B) {
	if !Available() {
		b.Skip("no accelerator")
	}
	s, _ := NewScanner(benchPatterns)
	defer s.Close()
	payload := makePayload(262144)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.ScanFirst(payload)
	}
}
