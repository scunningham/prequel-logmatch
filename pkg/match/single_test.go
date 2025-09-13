package match

import (
	"testing"

	"github.com/prequel-dev/prequel-logmatch/pkg/entry"
)

func TestSingleSimple(t *testing.T) {

	m, err := NewMatchSingle(TermT{
		Type:  TermRaw,
		Value: "apple",
	})
	if err != nil {
		t.Fatalf("Failed to create matcher: %v", err)
	}

	hits := m.Scan(entry.LogEntry{Line: "apple"})
	if hits.Cnt != 1 {
		t.Errorf("Expected 1 hit, got %d", hits.Cnt)
	}

	hits = m.Scan(entry.LogEntry{Line: "banana"})
	if hits.Cnt != 0 {
		t.Errorf("Expected 0 hits, got %d", hits.Cnt)
	}
}

func TestSingleBadMatcher(t *testing.T) {

	_, err := NewMatchSingle(TermT{
		Type:  TermRaw,
		Value: "",
	})
	if err != ErrTermEmpty {
		t.Fatalf("Expected error, got %v", err)
	}
}

func TestNOOPS(t *testing.T) {
	m, err := NewMatchSingle(TermT{
		Type:  TermRaw,
		Value: "apple",
	})
	if err != nil {
		t.Fatalf("Failed to create matcher: %v", err)
	}

	hits := m.Eval(12345)
	if hits.Cnt != 0 {
		t.Errorf("Expected 0 hit, got %d", hits.Cnt)
	}

	m.GarbageCollect(12345)
}
