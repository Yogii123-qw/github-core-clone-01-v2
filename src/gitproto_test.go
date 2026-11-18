package main

import (
	"bytes"
	"strings"
	"testing"
)

// pushBody crafts a receive-pack request: ref-command pkt-lines, a flush,
// then the raw pack bytes.
func pushBody(t *testing.T, commands []string, pack []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, cmd := range commands {
		buf.Write(PktLine(cmd + "\n"))
	}
	buf.Write(PktFlush())
	buf.Write(pack)
	return buf.Bytes()
}

// packOf builds a pack containing the given objects from a staging repo.
func packOf(t *testing.T, repo *Repo, shas []string) []byte {
	t.Helper()
	var objs []PackObject
	for _, sha := range shas {
		obj := repo.Objects[sha]
		if obj == nil {
			t.Fatalf("object %s missing from staging repo", sha)
		}
		objs = append(objs, PackObject{Type: obj.Type, Data: obj.Data, SHA: sha})
	}
	pack, err := WritePack(repo, objs)
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func TestInfoRefsAdvertisements(t *testing.T) {
	repo, _, _, c, _ := testChain(t)
	repo.DefaultBranch = "main"
	repo.Refs["refs/heads/main"] = c

	upload := HandleInfoRefs(repo, "git-upload-pack", "")
	if upload.Status != 200 {
		t.Fatalf("upload advertisement status = %d", upload.Status)
	}
	if !strings.Contains(string(upload.Body), "# service=git-upload-pack\n") {
		t.Fatalf("upload advertisement missing service line: %q", upload.Body)
	}
	if !strings.Contains(string(upload.Body), c+" HEAD") || !strings.Contains(string(upload.Body), "refs/heads/main") {
		t.Fatalf("upload advertisement missing refs: %q", upload.Body)
	}

	receive := HandleInfoRefs(repo, "git-receive-pack", "")
	if receive.Status != 200 || !strings.Contains(string(receive.Body), "# service=git-receive-pack\n") {
		t.Fatalf("receive advertisement = %d %q", receive.Status, receive.Body)
	}

	v2 := HandleInfoRefs(repo, "git-upload-pack", "version=2")
	if v2.Status != 200 {
		t.Fatalf("v2 advertisement status = %d", v2.Status)
	}
	body := string(v2.Body)
	for _, want := range []string{"version 2\n", "ls-refs=unborn\n", "fetch=shallow\n", "object-format=sha1\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("v2 advertisement missing %q: %q", want, body)
		}
	}

	if got := HandleInfoRefs(nil, "git-upload-pack", ""); got.Status != 404 {
		t.Fatalf("nil repo status = %d, want 404", got.Status)
	}
	if got := HandleInfoRefs(repo, "git-unknown", ""); got.Status != 404 {
		t.Fatalf("unknown service status = %d, want 404", got.Status)
	}

	// Empty repo: capabilities^{} line with the zero sha.
	empty := HandleInfoRefs(testRepo(), "git-upload-pack", "")
	if !strings.Contains(string(empty.Body), ZERO_SHA+" capabilities^{}") {
		t.Fatalf("empty repo advertisement = %q", empty.Body)
	}
}

func TestReceivePackFastForward(t *testing.T) {
	// Staging repo holds the objects; target repo holds only commit A.
	stage, a, b, _, _ := testChain(t)
	target := testRepo()
	target.DefaultBranch = "main"
	target.Refs["refs/heads/main"] = a
	// Seed the target with A's objects only.
	for _, sha := range []string{a} {
		obj := stage.Objects[sha]
		target.Objects[sha] = obj
	}
	commitA := GetCommit(stage, a)
	target.Objects[commitA.Tree] = stage.Objects[commitA.Tree]
	for _, e := range GetTree(stage, commitA.Tree) {
		target.Objects[e.SHA] = stage.Objects[e.SHA]
	}
	// Pack B plus its tree and blobs.
	commitB := GetCommit(stage, b)
	shas := []string{b, commitB.Tree}
	for _, e := range GetTree(stage, commitB.Tree) {
		shas = append(shas, e.SHA)
	}
	body := pushBody(t, []string{a + " " + b + " refs/heads/main"}, packOf(t, stage, shas))
	resp := HandleReceivePack(target, body)
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "unpack ok") || !strings.Contains(string(resp.Body), "ok refs/heads/main") {
		t.Fatalf("fast-forward report = %q", resp.Body)
	}
	if target.Refs["refs/heads/main"] != b {
		t.Fatalf("ref = %s, want %s", target.Refs["refs/heads/main"], b)
	}
	if target.Objects[b] == nil {
		t.Fatal("pushed commit missing from the target store")
	}
}

func TestReceivePackNonFastForward(t *testing.T) {
	stage, a, b, _, d := testChain(t)
	target := testRepo()
	target.DefaultBranch = "main"
	target.Refs["refs/heads/main"] = b
	// Seed the target with the full chain so only the FF check can fail.
	for sha, obj := range stage.Objects {
		target.Objects[sha] = obj
	}
	// Push side-branch commit D (child of A, not a descendant of B).
	commitD := GetCommit(stage, d)
	shas := []string{d, commitD.Tree}
	body := pushBody(t, []string{b + " " + d + " refs/heads/main"}, packOf(t, stage, shas))
	resp := HandleReceivePack(target, body)
	if !strings.Contains(string(resp.Body), "ng refs/heads/main non-fast-forward") {
		t.Fatalf("non-FF report = %q", resp.Body)
	}
	if target.Refs["refs/heads/main"] != b {
		t.Fatal("ref must not move on a non-fast-forward push")
	}
	// Stale old sha: also non-fast-forward.
	body2 := pushBody(t, []string{a + " " + d + " refs/heads/main"}, packOf(t, stage, shas))
	resp2 := HandleReceivePack(target, body2)
	if !strings.Contains(string(resp2.Body), "non-fast-forward") {
		t.Fatalf("stale-old report = %q", resp2.Body)
	}
}

func TestReceivePackDelete(t *testing.T) {
	stage, a, _, _, _ := testChain(t)
	target := testRepo()
	target.Refs["refs/heads/main"] = a
	for sha, obj := range stage.Objects {
		target.Objects[sha] = obj
	}
	body := pushBody(t, []string{a + " " + ZERO_SHA + " refs/heads/main"}, nil)
	resp := HandleReceivePack(target, body)
	if !strings.Contains(string(resp.Body), "ok refs/heads/main") {
		t.Fatalf("delete report = %q", resp.Body)
	}
	if _, ok := target.Refs["refs/heads/main"]; ok {
		t.Fatal("ref must be deleted")
	}
	// Deleting a missing ref fails.
	body2 := pushBody(t, []string{ZERO_SHA + " " + ZERO_SHA + " refs/heads/gone"}, nil)
	resp2 := HandleReceivePack(target, body2)
	if !strings.Contains(string(resp2.Body), "remote ref does not exist") {
		t.Fatalf("delete-missing report = %q", resp2.Body)
	}
}

func TestReceivePackBadPack(t *testing.T) {
	stage, a, b, _, _ := testChain(t)
	target := testRepo()
	target.Refs["refs/heads/main"] = a
	for sha, obj := range stage.Objects {
		target.Objects[sha] = obj
	}
	body := pushBody(t, []string{a + " " + b + " refs/heads/main"}, []byte("PACKgarbage"))
	resp := HandleReceivePack(target, body)
	if !strings.Contains(string(resp.Body), "unpack ") || strings.Contains(string(resp.Body), "unpack ok") {
		t.Fatalf("bad-pack report = %q", resp.Body)
	}
	if !strings.Contains(string(resp.Body), "unpacker error") {
		t.Fatalf("bad-pack report must blame the unpacker: %q", resp.Body)
	}
	if target.Refs["refs/heads/main"] != a {
		t.Fatal("ref must not move when the pack fails to unpack")
	}
}

func TestReceivePackSideband(t *testing.T) {
	stage, a, b, _, _ := testChain(t)
	target := testRepo()
	target.Refs["refs/heads/main"] = a
	for sha, obj := range stage.Objects {
		target.Objects[sha] = obj
	}
	commitB := GetCommit(stage, b)
	body := pushBody(t, []string{a + " " + b + " refs/heads/main\x00report-status side-band-64k"}, packOf(t, stage, []string{b, commitB.Tree}))
	resp := HandleReceivePack(target, body)
	lines, _, sawFlush := ParsePktLines(resp.Body)
	if !sawFlush || len(lines) == 0 {
		t.Fatalf("sideband response not pkt-framed: %q", resp.Body)
	}
	for _, line := range lines {
		if len(line) == 0 || line[0] != 1 {
			t.Fatalf("sideband line missing band byte 1: %q", line)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "unpack ok") || !strings.Contains(joined, "ok refs/heads/main") {
		t.Fatalf("sideband report = %q", joined)
	}
}

func TestUploadPackV0(t *testing.T) {
	repo, _, _, c, _ := testChain(t)
	body := append(PktLine("want "+c+"\n"), PktFlush()...)
	body = append(body, PktLine("done\n")...)
	resp := HandleUploadPackV0(repo, body)
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	lines, _, sawFlush := ParsePktLines(resp.Body)
	if !sawFlush {
		t.Fatal("upload-pack response must end with a flush")
	}
	if len(lines) == 0 || lines[0] != "NAK\n" {
		t.Fatalf("first line = %q, want NAK", lines)
	}
	// The sideband payload (band 1) is a pack containing C.
	var pack []byte
	for _, line := range lines[1:] {
		if len(line) > 0 && line[0] == 1 {
			pack = append(pack, line[1:]...)
		}
	}
	parsed, err := ReadPack(pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, obj := range parsed {
		if obj.SHA == c {
			found = true
		}
	}
	if !found {
		t.Fatalf("pack of %d objects missing the wanted commit", len(parsed))
	}
}

func TestMatchIdent(t *testing.T) {
	// matchIdent mirrors the old ident regex's leftmost match:
	// "Name <email> timestamp tz".
	name, email, digits, ok := matchIdent("Ada Lovelace <ada@x> 1700000000 +0000")
	if !ok || name != "Ada Lovelace" || email != "ada@x" || digits != "1700000000" {
		t.Fatalf("matchIdent = %q, %q, %q, %v", name, email, digits, ok)
	}
	// No angle brackets: no match.
	if _, _, _, ok := matchIdent("no ident here"); ok {
		t.Fatal("plain text must not match an ident")
	}
	// Malformed tail after the brackets: no match.
	if _, _, _, ok := matchIdent("Ada <ada@x> not-a-date"); ok {
		t.Fatal("ident with a bad tail must not match")
	}
	// identTail matches " <digits> <tz>" against the whole remainder.
	d, ok := identTail(" 1700000000 +0000")
	if !ok || d != "1700000000" {
		t.Fatalf("identTail = %q, %v", d, ok)
	}
	if _, ok := identTail("1700000000 +0000"); ok {
		t.Fatal("identTail requires a leading space")
	}
	if _, ok := identTail(" 1700000000 +000"); ok {
		t.Fatal("identTail requires a 4-digit timezone")
	}
	if _, ok := identTail(" 12x +0000"); ok {
		t.Fatal("identTail requires all-digit timestamps")
	}
}

func TestMatchIdentDate(t *testing.T) {
	digits, ok := matchIdentDate("Ada <ada@x> 1700000000 +0000")
	if !ok || digits != "1700000000" {
		t.Fatalf("matchIdentDate = %q, %v", digits, ok)
	}
	if _, ok := matchIdentDate("Ada <ada@x> 1700000000"); ok {
		t.Fatal("missing timezone must not match")
	}
	if _, ok := matchIdentDate("Ada <ada@x> notanum +0000"); ok {
		t.Fatal("non-numeric timestamp must not match")
	}
	if _, ok := matchIdentDate("plain text"); ok {
		t.Fatal("plain text must not match")
	}
	if _, ok := matchIdentDate("Ada <ada@x> 1700000000 +0000 "); ok {
		t.Fatal("trailing space must not match")
	}
}

func TestMatchGitRoute(t *testing.T) {
	owner, repo, action, ok := matchGitRoute("/adaiversen/myrepo.git/info/refs")
	if !ok || owner != "adaiversen" || repo != "myrepo" || action != "info/refs" {
		t.Fatalf("matchGitRoute = %q %q %q %v", owner, repo, action, ok)
	}
	_, _, action, ok = matchGitRoute("/adaiversen/myrepo.git/git-upload-pack")
	if !ok || action != "git-upload-pack" {
		t.Fatalf("upload-pack route = %q %v", action, ok)
	}
	bad := []string{
		"/adaiversen/myrepo/info/refs",    // missing .git
		"/adaiversen/myrepo.git",          // no action
		"/onlyone",                        // single segment
		"/a/b/c.git/info/refs",            // slash inside the repo name
		"/adaiversen/myrepo.git/other",    // unknown action
		"adaiversen/myrepo.git/info/refs", // no leading slash
		"",
	}
	for _, path := range bad {
		if _, _, _, ok := matchGitRoute(path); ok {
			t.Fatalf("matchGitRoute(%q) must not match", path)
		}
	}
}
