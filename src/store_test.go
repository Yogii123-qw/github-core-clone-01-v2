package main

import (
	"strings"
	"testing"
)

func TestGlobalIDRoundTrip(t *testing.T) {
	kinds := []struct {
		kind  string
		parts []string
	}{
		{"Repository", []string{"7"}},
		{"Issue", []string{"7", "3"}},
		{"PullRequest", []string{"7", "2"}},
		{"User", []string{"adaiversen"}},
		{"IssueComment", []string{"7", "3", "1"}},
		{"Label", []string{"7", "bug"}},
	}
	for _, k := range kinds {
		id := GlobalID(k.kind, k.parts...)
		kind, parts, ok := ParseGlobalID(id)
		if !ok {
			t.Fatalf("ParseGlobalID(%q) rejected a forge id", id)
		}
		if kind != k.kind {
			t.Fatalf("kind = %q, want %q", kind, k.kind)
		}
		if len(parts) != len(k.parts) {
			t.Fatalf("parts = %v, want %v", parts, k.parts)
		}
		for i := range parts {
			if parts[i] != k.parts[i] {
				t.Fatalf("parts = %v, want %v", parts, k.parts)
			}
		}
	}
}

func TestParseGlobalIDRejects(t *testing.T) {
	bad := []string{
		"",
		"not-base64!!!",
		"aGVsbG8=",         // "hello", no colon
		"V2lkZ2V0OjE=",     // "Widget:1", unknown kind
		"UmVwb3NpdG9yeQ==", // "Repository", no parts
		"repository:1",     // not base64
	}
	for _, id := range bad {
		if _, _, ok := ParseGlobalID(id); ok {
			t.Fatalf("ParseGlobalID(%q) accepted a non-forge id", id)
		}
	}
}

func TestStoreSeqAndISO(t *testing.T) {
	s := NewStore()
	first := s.NextSeq()
	second := s.NextSeq()
	if second != first+1 {
		t.Fatalf("seqs not consecutive: %d, %d", first, second)
	}
	iso1 := s.ISO(first)
	iso2 := s.ISO(second)
	if iso1 == iso2 {
		t.Fatal("different seqs must give different timestamps")
	}
	if !strings.HasSuffix(iso1, "Z") || !strings.Contains(iso1, "T") {
		t.Fatalf("ISO timestamp has wrong shape: %q", iso1)
	}
	// Whole-second steps: the two timestamps differ by exactly 1 second
	// in their seconds field.
	if s.UnixSeconds(second)-s.UnixSeconds(first) != 1 {
		t.Fatal("UnixSeconds must step by 1 per seq")
	}
	if s.ISO(first) != iso1 {
		t.Fatal("ISO must be deterministic for a given seq")
	}
}

func TestStoreRepoLifecycle(t *testing.T) {
	s := NewStore()
	if s.GetRepo("missing") != nil {
		t.Fatal("GetRepo of a missing repo must return nil")
	}
	repo := s.CreateRepo("demo", "d", false)
	if repo == nil || repo.Name != "demo" {
		t.Fatalf("CreateRepo returned %+v", repo)
	}
	if s.GetRepo("demo") != repo {
		t.Fatal("GetRepo must return the created repo")
	}
	if !s.DeleteRepo("demo") {
		t.Fatal("DeleteRepo of an existing repo must return true")
	}
	if s.GetRepo("demo") != nil {
		t.Fatal("deleted repo must be gone")
	}
	if s.DeleteRepo("demo") {
		t.Fatal("DeleteRepo of a missing repo must return false")
	}
	// DBIDs increase across repos.
	a := s.CreateRepo("a", "", false)
	b := s.CreateRepo("b", "", false)
	if b.DBID <= a.DBID {
		t.Fatalf("DBIDs not increasing: %d, %d", a.DBID, b.DBID)
	}
}

func TestNextIDIncreasing(t *testing.T) {
	s := NewStore()
	prev := s.NextID()
	for i := 0; i < 5; i++ {
		cur := s.NextID()
		if cur <= prev {
			t.Fatalf("NextID not increasing: %d then %d", prev, cur)
		}
		prev = cur
	}
}
