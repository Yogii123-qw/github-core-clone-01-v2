package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"
)

func testRepo() *Repo {
	return NewRepo("r", "", false, 1, 0)
}

func testChain(t *testing.T) (*Repo, string, string, string, string) {
	t.Helper()
	repo := testRepo()
	blobA := StoreObject(repo, ObjectBlob, []byte("a\n"))
	treeA := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "a.txt", SHA: blobA, Kind: "blob"}})
	commitA := BuildCommit(repo, treeA, nil, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "A\n")
	blobB := StoreObject(repo, ObjectBlob, []byte("b\n"))
	treeB := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "a.txt", SHA: blobA, Kind: "blob"}, {Mode: "100644", Name: "b.txt", SHA: blobB, Kind: "blob"}})
	commitB := BuildCommit(repo, treeB, []string{commitA}, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "B\n")
	blobC := StoreObject(repo, ObjectBlob, []byte("c\n"))
	treeC := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "c.txt", SHA: blobC, Kind: "blob"}})
	commitC := BuildCommit(repo, treeC, []string{commitB}, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "C\n")
	commitD := BuildCommit(repo, treeB, []string{commitA}, "A <a@x> 0 +0000", "A <a@x> 0 +0000", "D\n")
	return repo, commitA, commitB, commitC, commitD
}

func TestHashObjectDeterminism(t *testing.T) {
	sha1a := HashObject(ObjectBlob, []byte("hello\n"))
	sha1b := HashObject(ObjectBlob, []byte("hello\n"))
	if sha1a != sha1b {
		t.Fatalf("same input gave different hashes: %s vs %s", sha1a, sha1b)
	}
	if len(sha1a) != 40 || sha1a != strings.ToLower(sha1a) {
		t.Fatalf("hash is not 40 lowercase hex: %q", sha1a)
	}
	if HashObject(ObjectTree, []byte("hello\n")) == sha1a {
		t.Fatal("object type must affect the hash")
	}
	if HashObject(ObjectBlob, []byte("hello")) == sha1a {
		t.Fatal("object data must affect the hash")
	}
	// Known git value: blob "hello\n" is a well-known hash.
	if sha1a != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("blob hash mismatch with git: got %s", sha1a)
	}
}

func TestStoreObjectRoundTrip(t *testing.T) {
	repo := testRepo()
	sha := StoreObject(repo, ObjectBlob, []byte("payload"))
	obj, ok := repo.Objects[sha]
	if !ok {
		t.Fatal("stored object not found")
	}
	if obj.Type != ObjectBlob || string(obj.Data) != "payload" {
		t.Fatalf("stored object mismatch: %+v", obj)
	}
}

func TestBuildTreeSortOrder(t *testing.T) {
	repo := testRepo()
	blob := StoreObject(repo, ObjectBlob, []byte("x"))
	sub := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "inner.txt", SHA: blob, Kind: "blob"}})
	// Git sorts directory "dir" as "dir/", which comes AFTER "dir.txt".
	tree := BuildTree(repo, []TreeEntry{
		{Mode: "40000", Name: "dir", SHA: sub, Kind: "tree"},
		{Mode: "100644", Name: "dir.txt", SHA: blob, Kind: "blob"},
		{Mode: "100644", Name: "a.txt", SHA: blob, Kind: "blob"},
	})
	entries := ParseTree(repo.Objects[tree].Data)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	names := []string{entries[0].Name, entries[1].Name, entries[2].Name}
	want := []string{"a.txt", "dir.txt", "dir"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tree order = %v, want %v", names, want)
		}
	}
	if entries[2].Mode != "40000" || entries[2].Kind != "tree" {
		t.Fatalf("directory entry lost its mode/kind: %+v", entries[2])
	}
}

func TestBuildCommitAndGetCommit(t *testing.T) {
	repo, a, b, _, _ := testChain(t)
	info := GetCommit(repo, b)
	if info == nil {
		t.Fatal("GetCommit returned nil")
	}
	if len(info.Parents) != 1 || info.Parents[0] != a {
		t.Fatalf("parents = %v, want [%s]", info.Parents, a)
	}
	if info.Message != "B\n" {
		t.Fatalf("message = %q, want %q", info.Message, "B\n")
	}
	if GetCommit(repo, strings.Repeat("0", 40)) != nil {
		t.Fatal("GetCommit of a missing sha must return nil")
	}
}

func TestApplyDeltaInsertAndCopy(t *testing.T) {
	base := []byte("hello world")
	// Delta: source size 11, target size 15, insert "say ", copy 11 from 0.
	delta := []byte{0x0b, 0x0f, 0x04, 's', 'a', 'y', ' ', 0x90, 0x0b}
	out, err := ApplyDelta(base, delta)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "say hello world" {
		t.Fatalf("delta result = %q", out)
	}
}

func TestApplyDeltaErrors(t *testing.T) {
	base := []byte("hello world")
	cases := []struct {
		name  string
		delta []byte
	}{
		{"empty", nil},
		{"truncated-size", []byte{0x0b}},
		{"copy-beyond-base", []byte{0x0b, 0x0b, 0x91, 0x05, 0x0b}}, // copy 11 from offset 5
		{"target-size-mismatch", []byte{0x0b, 0x05, 0x04, 's', 'a', 'y', ' '}},
		{"zero-opcode", []byte{0x0b, 0x0b, 0x00}},
		{"insert-truncated", []byte{0x0b, 0x0f, 0x04, 's', 'a'}},
	}
	for _, tc := range cases {
		if _, err := ApplyDelta(base, tc.delta); err == nil {
			t.Fatalf("%s: expected error, got nil", tc.name)
		}
	}
}

func zlibDeflate(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// packEntry builds one pack object entry: type+size varint header plus a
// zlib-deflated payload. extra is inserted between the header and the
// payload (ofs-delta offset or ref-delta base sha).
func packEntry(t *testing.T, typ, size int, extra, payload []byte) []byte {
	t.Helper()
	var hdr []byte
	first := byte((typ << 4) | (size & 0x0f))
	size >>= 4
	if size > 0 {
		first |= 0x80
	}
	hdr = append(hdr, first)
	for size > 0 {
		b := byte(size & 0x7f)
		size >>= 7
		if size > 0 {
			b |= 0x80
		}
		hdr = append(hdr, b)
	}
	out := append(hdr, extra...)
	return append(out, zlibDeflate(t, payload)...)
}

func finishPack(t *testing.T, entries [][]byte, count int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("PACK")
	buf.Write([]byte{0, 0, 0, 2})
	buf.Write([]byte{byte(count >> 24), byte(count >> 16), byte(count >> 8), byte(count)})
	for _, e := range entries {
		buf.Write(e)
	}
	sum := sha1.Sum(buf.Bytes())
	return append(buf.Bytes(), sum[:]...)
}

func TestPackRoundTripNoDelta(t *testing.T) {
	repo, _, _, c, _ := testChain(t)
	// Collect every object in the store and round-trip it.
	var objs []PackObject
	for sha, obj := range repo.Objects {
		objs = append(objs, PackObject{Type: obj.Type, Data: obj.Data, SHA: sha})
	}
	pack, err := WritePack(repo, objs)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadPack(pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(objs) {
		t.Fatalf("parsed %d objects, want %d", len(parsed), len(objs))
	}
	seen := map[string]PackObject{}
	for _, p := range parsed {
		seen[p.SHA] = p
	}
	for _, o := range objs {
		got, ok := seen[o.SHA]
		if !ok {
			t.Fatalf("object %s missing after round-trip", o.SHA)
		}
		if got.Type != o.Type || !bytes.Equal(got.Data, o.Data) {
			t.Fatalf("object %s mismatch after round-trip", o.SHA)
		}
	}
	_ = c
}

func TestPackWithOfsDelta(t *testing.T) {
	base := []byte("hello world")
	delta := []byte{0x0b, 0x0f, 0x04, 's', 'a', 'y', ' ', 0x90, 0x0b}
	baseEntry := packEntry(t, 3, len(base), nil, base)
	// Ofs-delta: negative offset from the delta entry back to the base
	// entry. The delta entry starts right after the base entry, so the
	// offset is len(baseEntry). Encoding: 7-bit groups, big-endian with
	// the continuation rule of pack ofs-deltas.
	off := len(baseEntry)
	var offBytes []byte
	offBytes = append(offBytes, byte(off&0x7f))
	off >>= 7
	for off > 0 {
		off--
		offBytes = append([]byte{byte(0x80 | (off & 0x7f))}, offBytes...)
		off >>= 7
	}
	deltaEntry := packEntry(t, 6, len(delta), offBytes, delta)
	pack := finishPack(t, [][]byte{baseEntry, deltaEntry}, 2)
	parsed, err := ReadPack(pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed %d objects, want 2", len(parsed))
	}
	if string(parsed[1].Data) != "say hello world" || parsed[1].Type != ObjectBlob {
		t.Fatalf("ofs-delta resolution wrong: %+v", parsed[1])
	}
}

func TestPackWithRefDelta(t *testing.T) {
	repo := testRepo()
	base := []byte("hello world")
	baseSHA := StoreObject(repo, ObjectBlob, base)
	delta := []byte{0x0b, 0x0f, 0x04, 's', 'a', 'y', ' ', 0x90, 0x0b}
	raw, err := hex.DecodeString(baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	deltaEntry := packEntry(t, 7, len(delta), raw, delta)
	pack := finishPack(t, [][]byte{deltaEntry}, 1)
	parsed, err := ReadPack(pack, func(sha string) *GitObject { return repo.Objects[sha] })
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || string(parsed[0].Data) != "say hello world" {
		t.Fatalf("ref-delta resolution wrong: %+v", parsed)
	}
	// Without a resolver the ref-delta base is missing: must error.
	if _, err := ReadPack(pack, nil); err == nil {
		t.Fatal("ref-delta with no resolver must error")
	}
}

func TestPackMalformed(t *testing.T) {
	repo, _, _, _, _ := testChain(t)
	var objs []PackObject
	for sha, obj := range repo.Objects {
		objs = append(objs, PackObject{Type: obj.Type, Data: obj.Data, SHA: sha})
	}
	good, err := WritePack(repo, objs)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		pack []byte
	}{
		{"too-short", []byte("PACK")},
		{"bad-magic", append([]byte("XXXX"), good[4:]...)},
		{"bad-version", append(append([]byte("PACK"), 0, 0, 0, 3), good[8:]...)},
		{"bad-trailer", append(append([]byte{}, good[:len(good)-20]...), bytes.Repeat([]byte{0}, 20)...)},
		{"truncated", good[:len(good)/2]},
	}
	for _, tc := range cases {
		if _, err := ReadPack(tc.pack, nil); err == nil {
			t.Fatalf("%s: expected error, got nil", tc.name)
		}
	}
}

func TestFlattenAndTreeFromFlat(t *testing.T) {
	repo := testRepo()
	blob1 := StoreObject(repo, ObjectBlob, []byte("one"))
	blob2 := StoreObject(repo, ObjectBlob, []byte("two"))
	sub := BuildTree(repo, []TreeEntry{{Mode: "100644", Name: "deep.txt", SHA: blob2, Kind: "blob"}})
	root := BuildTree(repo, []TreeEntry{
		{Mode: "100644", Name: "top.txt", SHA: blob1, Kind: "blob"},
		{Mode: "40000", Name: "sub", SHA: sub, Kind: "tree"},
	})
	flat := FlattenTree(repo, root)
	if len(flat) != 2 {
		t.Fatalf("flat map has %d entries, want 2", len(flat))
	}
	if flat["top.txt"].SHA != blob1 || flat["sub/deep.txt"].SHA != blob2 {
		t.Fatalf("flat map wrong: %+v", flat)
	}
	rebuilt := TreeFromFlat(repo, flat)
	if rebuilt != root {
		t.Fatalf("rebuild gave %s, want %s", rebuilt, root)
	}
}

func TestIsAncestorAndMergeBase(t *testing.T) {
	repo, a, b, c, d := testChain(t)
	if !IsAncestor(repo, a, c) || !IsAncestor(repo, b, c) {
		t.Fatal("chain ancestors not recognized")
	}
	if !IsAncestor(repo, c, c) {
		t.Fatal("a commit must be its own ancestor")
	}
	if IsAncestor(repo, c, a) || IsAncestor(repo, d, c) || IsAncestor(repo, c, d) {
		t.Fatal("non-ancestors reported as ancestors")
	}
	if got := MergeBase(repo, c, d); got != a {
		t.Fatalf("mergeBase(C, D) = %s, want %s", got, a)
	}
	if got := MergeBase(repo, c, c); got != c {
		t.Fatalf("mergeBase(C, C) = %s, want %s", got, c)
	}
	if got := MergeBase(repo, b, c); got != b {
		t.Fatalf("mergeBase(B, C) = %s, want %s", got, b)
	}
}

func TestCollectForFetch(t *testing.T) {
	repo, a, b, c, d := testChain(t)
	// Full fetch: every object reachable from C. The store holds 10
	// objects, but commit D is a side branch unreachable from C, so the
	// fetch carries 9.
	full := CollectForFetch(repo, []string{c}, nil, FetchOptions{})
	if len(full.Objects) != 9 {
		t.Fatalf("full fetch = %d objects, want 9 reachable from C", len(full.Objects))
	}
	fullShas := map[string]bool{}
	for _, o := range full.Objects {
		fullShas[o.SHA] = true
	}
	if fullShas[d] {
		t.Fatal("unreachable side-branch commit D must not be fetched")
	}
	// Haves exclude B and everything below it.
	withHaves := CollectForFetch(repo, []string{c}, map[string]bool{b: true}, FetchOptions{})
	shas := map[string]bool{}
	for _, o := range withHaves.Objects {
		shas[o.SHA] = true
	}
	if !shas[c] {
		t.Fatal("want C missing with haves")
	}
	if shas[b] || shas[a] {
		t.Fatal("haves must exclude B and A")
	}
	// Depth 1: only C plus its tree and blobs; C is the shallow boundary.
	depth := 1
	shallow := CollectForFetch(repo, []string{c}, nil, FetchOptions{Depth: &depth})
	shas = map[string]bool{}
	for _, o := range shallow.Objects {
		shas[o.SHA] = true
	}
	if !shas[c] || shas[b] {
		t.Fatalf("depth-1 fetch wrong: has C=%v has B=%v", shas[c], shas[b])
	}
	if len(shallow.Shallow) != 1 || shallow.Shallow[0] != c {
		t.Fatalf("depth-1 shallow = %v, want [%s]", shallow.Shallow, c)
	}
	// Deepen, mirroring the kit's fetch-shallow-deepen scenario: the
	// client holds B as its shallow boundary and fetches the new tip C
	// with deepen 2. A (the generation beyond the boundary) is
	// delivered, B is not re-delivered (it is a have), and B is
	// unshallowed because its parent is now included.
	deepen := 2
	deepened := CollectForFetch(repo, []string{c}, map[string]bool{b: true}, FetchOptions{Deepen: &deepen, Shallows: map[string]bool{b: true}})
	shas = map[string]bool{}
	for _, o := range deepened.Objects {
		shas[o.SHA] = true
	}
	if !shas[c] || !shas[a] || shas[b] {
		t.Fatalf("deepen fetch wrong: has C=%v has A=%v has B=%v", shas[c], shas[a], shas[b])
	}
	unshallowed := map[string]bool{}
	for _, s := range deepened.Unshallow {
		unshallowed[s] = true
	}
	if !unshallowed[b] {
		t.Fatalf("deepen unshallow = %v, want B unshallowed", deepened.Unshallow)
	}
}

func TestPktLineAndParse(t *testing.T) {
	if got := string(PktLine("hello\n")); got != "000ahello\n" {
		t.Fatalf("PktLine = %q", got)
	}
	buf := append(PktLine("one\n"), PktLine("two\n")...)
	buf = append(buf, PktFlush()...)
	lines, consumed, sawFlush := ParsePktLines(buf)
	if !sawFlush || consumed != len(buf) {
		t.Fatalf("flush parse: consumed=%d/%d sawFlush=%v", consumed, len(buf), sawFlush)
	}
	if len(lines) != 2 || lines[0] != "one\n" || lines[1] != "two\n" {
		t.Fatalf("lines = %q", lines)
	}
	// Delimiter packets are skipped.
	buf2 := append(PktLine("a\n"), []byte("0001")...)
	buf2 = append(buf2, PktLine("b\n")...)
	lines2, _, sawFlush2 := ParsePktLines(buf2)
	if sawFlush2 || len(lines2) != 2 || lines2[1] != "b\n" {
		t.Fatalf("delimiter parse: lines=%q sawFlush=%v", lines2, sawFlush2)
	}
	// Malformed input stops parsing without error and without consuming.
	lines3, consumed3, _ := ParsePktLines([]byte("zzzzgarbage"))
	if len(lines3) != 0 || consumed3 != 0 {
		t.Fatalf("malformed parse: lines=%q consumed=%d", lines3, consumed3)
	}
	// Truncated length header stops parsing.
	lines4, consumed4, _ := ParsePktLines(append(PktLine("ok\n"), []byte("001")...))
	if len(lines4) != 1 || consumed4 != 7 {
		t.Fatalf("truncated parse: lines=%q consumed=%d", lines4, consumed4)
	}
}

func TestSidebandEncode(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 70000)
	enc := SidebandEncode(payload, 1)
	// Two data packets (65515 + 4485) plus flush.
	if !bytes.HasSuffix(enc, []byte("0000")) {
		t.Fatal("sideband output must end with a flush packet")
	}
	lines, _, sawFlush := ParsePktLines(enc)
	if !sawFlush || len(lines) != 2 {
		t.Fatalf("sideband packets = %d, sawFlush=%v", len(lines), sawFlush)
	}
	if lines[0][0] != 1 || len(lines[0]) != 65516 || len(lines[1]) != 4486 {
		t.Fatalf("sideband chunk sizes wrong: %d, %d", len(lines[0]), len(lines[1]))
	}
	empty := SidebandEncode(nil, 1)
	if string(empty) != "0000" {
		t.Fatalf("empty sideband = %q, want flush only", empty)
	}
}
