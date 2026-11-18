package main

import (
	"strings"
	"testing"
)

func opsKinds(ops []diffOp) []string {
	kinds := make([]string, len(ops))
	for i, op := range ops {
		kinds[i] = op.kind
	}
	return kinds
}

func TestLineDiff(t *testing.T) {
	// Identical files: all same.
	ops := lineDiff([]string{"a", "b"}, []string{"a", "b"})
	if len(ops) != 2 || ops[0].kind != "same" || ops[1].kind != "same" {
		t.Fatalf("identical diff = %+v", ops)
	}
	// Empty to content: all add.
	ops = lineDiff(nil, []string{"a", "b"})
	if len(ops) != 2 || ops[0].kind != "add" || ops[1].kind != "add" {
		t.Fatalf("empty->content diff = %+v", ops)
	}
	// Content to empty: all del.
	ops = lineDiff([]string{"a", "b"}, nil)
	if len(ops) != 2 || ops[0].kind != "del" || ops[1].kind != "del" {
		t.Fatalf("content->empty diff = %+v", ops)
	}
	// Middle replacement: del then add between sames.
	ops = lineDiff([]string{"a", "b", "c"}, []string{"a", "X", "c"})
	kinds := opsKinds(ops)
	want := []string{"same", "del", "add", "same"}
	if len(kinds) != len(want) {
		t.Fatalf("replacement diff kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("replacement diff kinds = %v, want %v", kinds, want)
		}
	}
	// Both empty: no ops.
	if ops := lineDiff(nil, nil); len(ops) != 0 {
		t.Fatalf("empty diff = %+v", ops)
	}
}

func TestLineDiffDegradesOnHugeInputs(t *testing.T) {
	oldLines := make([]string, 1001)
	newLines := make([]string, 1001)
	for i := range oldLines {
		oldLines[i] = "old"
		newLines[i] = "new"
	}
	ops := lineDiff(oldLines, newLines)
	// Whole-file replacement: all dels then all adds, no sames.
	if len(ops) != 2002 {
		t.Fatalf("degraded diff has %d ops, want 2002", len(ops))
	}
	for i, op := range ops {
		if i < 1001 && op.kind != "del" {
			t.Fatalf("op %d = %s, want del", i, op.kind)
		}
		if i >= 1001 && op.kind != "add" {
			t.Fatalf("op %d = %s, want add", i, op.kind)
		}
	}
}

func TestBuildPatch(t *testing.T) {
	ops := lineDiff([]string{"a", "b"}, []string{"a", "X", "b"})
	patch := buildPatch("f.txt", "f.txt", ops)
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	if lines[0] != "--- a/f.txt" || lines[1] != "+++ b/f.txt" {
		t.Fatalf("patch headers = %q", lines[:2])
	}
	if lines[2] != "@@ -1,2 +1,3 @@" {
		t.Fatalf("patch hunk header = %q", lines[2])
	}
	if lines[3] != " a" || lines[4] != "+X" || lines[5] != " b" || len(lines) != 6 {
		t.Fatalf("patch body = %q", lines[3:])
	}
}

func TestDiffTrees(t *testing.T) {
	repo := testRepo()
	blobA1 := StoreObject(repo, ObjectBlob, []byte("1\n2\n"))
	blobA2 := StoreObject(repo, ObjectBlob, []byte("1\n3\n2\n"))
	blobB := StoreObject(repo, ObjectBlob, []byte("gone\n"))
	blobC := StoreObject(repo, ObjectBlob, []byte("new1\nnew2\n"))
	base := BuildTree(repo, []TreeEntry{
		{Mode: "100644", Name: "a.txt", SHA: blobA1, Kind: "blob"},
		{Mode: "100644", Name: "b.txt", SHA: blobB, Kind: "blob"},
	})
	head := BuildTree(repo, []TreeEntry{
		{Mode: "100644", Name: "a.txt", SHA: blobA2, Kind: "blob"},
		{Mode: "100644", Name: "c.txt", SHA: blobC, Kind: "blob"},
	})
	diffs := DiffTrees(repo, base, head)
	if len(diffs) != 3 {
		t.Fatalf("diffs = %d, want 3", len(diffs))
	}
	byName := map[string]FileDiff{}
	for _, d := range diffs {
		byName[d.Filename] = d
	}
	if diffs[0].Filename != "a.txt" || diffs[1].Filename != "b.txt" || diffs[2].Filename != "c.txt" {
		t.Fatalf("diffs not sorted by path: %v", diffs)
	}
	a := byName["a.txt"]
	if a.Status != "modified" || a.Additions != 1 || a.Deletions != 0 || a.Changes != 1 {
		t.Fatalf("a.txt diff = %+v", a)
	}
	if a.Patch == nil || !strings.Contains(*a.Patch, "+3") {
		t.Fatalf("a.txt patch = %v", a.Patch)
	}
	b := byName["b.txt"]
	if b.Status != "removed" || b.Deletions != 1 || b.Additions != 0 {
		t.Fatalf("b.txt diff = %+v", b)
	}
	c := byName["c.txt"]
	if c.Status != "added" || c.Additions != 2 || c.Deletions != 0 {
		t.Fatalf("c.txt diff = %+v", c)
	}
	// Empty base: everything added.
	added := DiffTrees(repo, "", head)
	if len(added) != 2 {
		t.Fatalf("empty-base diffs = %d, want 2", len(added))
	}
	// Identical trees: no diffs.
	if same := DiffTrees(repo, base, base); len(same) != 0 {
		t.Fatalf("identical trees diff = %+v", same)
	}
}

func TestResolveRef(t *testing.T) {
	repo, a, b, c, _ := testChain(t)
	repo.DefaultBranch = "main"
	repo.Refs["refs/heads/main"] = c
	repo.Refs["refs/heads/feature"] = b
	repo.Refs["refs/tags/v1"] = a
	if got := ResolveRef(repo, "HEAD"); got != c {
		t.Fatalf("HEAD = %s, want %s", got, c)
	}
	if got := ResolveRef(repo, "main"); got != c {
		t.Fatalf("short branch = %s", got)
	}
	if got := ResolveRef(repo, "refs/heads/feature"); got != b {
		t.Fatalf("full ref = %s", got)
	}
	if got := ResolveRef(repo, "v1"); got != a {
		t.Fatalf("tag = %s", got)
	}
	if got := ResolveRef(repo, c); got != c {
		t.Fatalf("full sha = %s", got)
	}
	if got := ResolveRef(repo, c[:8]); got != c {
		t.Fatalf("unique prefix = %s", got)
	}
	if got := ResolveRef(repo, "nope"); got != "" {
		t.Fatalf("unknown ref = %s, want empty", got)
	}
	if got := ResolveRef(repo, strings.Repeat("0", 40)); got != "" {
		t.Fatalf("missing sha = %s, want empty", got)
	}
}

func TestParseLabelNames(t *testing.T) {
	names, ok := ParseLabelNames(nil, false)
	if !ok || len(names) != 0 {
		t.Fatalf("absent labels = %v, %v", names, ok)
	}
	names, ok = ParseLabelNames([]any{"bug", map[string]any{"name": "enh"}}, true)
	if !ok || len(names) != 2 || names[0] != "bug" || names[1] != "enh" {
		t.Fatalf("mixed labels = %v, %v", names, ok)
	}
	if _, ok := ParseLabelNames("bug", true); ok {
		t.Fatal("string labels must be rejected")
	}
	if _, ok := ParseLabelNames([]any{42}, true); ok {
		t.Fatal("numeric label must be rejected")
	}
	if _, ok := ParseLabelNames([]any{map[string]any{"id": 1}}, true); ok {
		t.Fatal("label object without a name must be rejected")
	}
}

func TestSplitHeadRef(t *testing.T) {
	owner, ref := SplitHeadRef("someone:feature")
	if owner != "someone" || ref != "feature" {
		t.Fatalf("SplitHeadRef = %q, %q", owner, ref)
	}
	owner, ref = SplitHeadRef("main")
	if ref != "main" {
		t.Fatalf("bare ref = %q, %q", owner, ref)
	}
}

func TestCreateIssueRecordLabels(t *testing.T) {
	store := NewStore()
	repo := store.CreateRepo("r", "", false)
	repo.Labels["bug"] = &Label{Name: "bug"}
	body := "hello"
	issue, errResult := CreateIssueRecord(store, repo, "t", &body, []string{"bug"})
	if errResult != nil || issue == nil || issue.Number != 1 {
		t.Fatalf("issue = %+v err = %+v", issue, errResult)
	}
	_, errResult = CreateIssueRecord(store, repo, "t2", nil, []string{"missing-label"})
	if errResult == nil || errResult.Status != 422 {
		t.Fatalf("unknown label must fail with 422, got %+v", errResult)
	}
}

func TestPRMergeInfoCleanAndConflict(t *testing.T) {
	store := NewStore()
	repo := store.CreateRepo("r", "", false)
	// Merge base A: a.txt = "x".
	blobX := StoreObject(repo, ObjectBlob, []byte("x\n"))
	treeA := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "a.txt", SHA: blobX, Kind: "blob"}})
	commitA := BuildCommit(repo, treeA, nil, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "A\n")
	// Head-only change: clean merge.
	blobH := StoreObject(repo, ObjectBlob, []byte("head\n"))
	treeH := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "a.txt", SHA: blobH, Kind: "blob"}})
	commitH := BuildCommit(repo, treeH, []string{commitA}, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "H\n")
	pull := &Pull{Number: 1, HeadSHA: commitH, BaseSHA: commitA, HeadRef: "feature", BaseRef: "main"}
	info := PRMergeInfo(store, repo, pull)
	if info == nil || !info.Mergeable || info.Conflict {
		t.Fatalf("clean merge info = %+v", info)
	}
	if info.Commits != 1 || info.MergeBase != commitA {
		t.Fatalf("clean merge counts = %+v", info)
	}
	if len(info.Files) != 1 || info.Files[0].Filename != "a.txt" {
		t.Fatalf("clean merge files = %+v", info.Files)
	}
	// Both sides change a.txt differently: conflict.
	blobB := StoreObject(repo, ObjectBlob, []byte("base\n"))
	treeB := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "a.txt", SHA: blobB, Kind: "blob"}})
	commitB := BuildCommit(repo, treeB, []string{commitA}, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "B\n")
	pull2 := &Pull{Number: 2, HeadSHA: commitH, BaseSHA: commitB, HeadRef: "feature", BaseRef: "main"}
	info2 := PRMergeInfo(store, repo, pull2)
	if info2 == nil || info2.Mergeable || !info2.Conflict {
		t.Fatalf("conflicting merge info = %+v", info2)
	}
}

func TestCreatePullRecordValidation(t *testing.T) {
	store := NewStore()
	repo := store.CreateRepo("r", "", false)
	blob := StoreObject(repo, ObjectBlob, []byte("x\n"))
	tree := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "a.txt", SHA: blob, Kind: "blob"}})
	commit := BuildCommit(repo, tree, nil, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "A\n")
	repo.Refs["refs/heads/main"] = commit
	// Unknown head.
	if _, errResult := CreatePullRecord(store, repo, "t", nil, "missing", "main"); errResult == nil {
		t.Fatal("unknown head must fail")
	}
	// Unknown base.
	if _, errResult := CreatePullRecord(store, repo, "t", nil, "main", "missing"); errResult == nil {
		t.Fatal("unknown base must fail")
	}
	// Same commit on both sides.
	if _, errResult := CreatePullRecord(store, repo, "t", nil, "main", "main"); errResult == nil {
		t.Fatal("identical head/base must fail")
	}
}

func TestHexMatchers(t *testing.T) {
	if !isHex40(strings.Repeat("a", 40)) || isHex40(strings.Repeat("a", 39)) || isHex40(strings.Repeat("g", 40)) || isHex40(strings.Repeat("A", 40)) {
		t.Fatal("isHex40 mismatch")
	}
	if !isHexPrefix("abc1234") || isHexPrefix("abc123") || isHexPrefix("") || isHexPrefix("xyz1234") || isHexPrefix(strings.Repeat("a", 40)) {
		t.Fatal("isHexPrefix mismatch")
	}
	if !isLowerHex('d') || !isLowerHex('9') || isLowerHex('G') || isLowerHex('g') {
		t.Fatal("isLowerHex mismatch")
	}
}

func TestValidRepoName(t *testing.T) {
	if !isValidRepoName("my-repo_1.2") || isValidRepoName("") || isValidRepoName("has space") || isValidRepoName("has/slash") {
		t.Fatal("isValidRepoName mismatch")
	}
}
