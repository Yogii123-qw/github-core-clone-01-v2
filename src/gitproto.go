// Git smart HTTP: info/refs advertisements (v0 and protocol v2),
// upload-pack (v0 and v2 fetch / ls-refs) and receive-pack (v0). All
// framing is pkt-line based; pack data travels over sideband-64k for v0
// upload-pack and for the receive-pack report. Callers must hold
// Store.mu.RLock for upload/info handlers and Store.mu.Lock for
// receive-pack (it mutates refs and objects).
package main

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
)

// GitResponse is a git smart-HTTP answer: binary body plus content type.
type GitResponse struct {
	Status       int
	ContentType  string
	Body         []byte
	ExtraHeaders map[string]string
}

// JSONType is the content type for JSON error bodies on git routes.
const JSONType = "application/json; charset=utf-8"

var noCacheHeaders = map[string]string{
	"expires":       "Fri, 01 Jan 1980 00:00:00 GMT",
	"pragma":        "no-cache",
	"cache-control": "no-cache, max-age=0, must-revalidate",
}

const uploadCaps = "multi_ack thin-pack side-band-64k ofs-delta include-tag agent=forge/1.0"
const receiveCaps = "report-status side-band-64k delete-refs ofs-delta agent=forge/1.0"

func symrefCap(repo *Repo) string {
	return "symref=HEAD:refs/heads/" + repo.DefaultBranch
}

func headSHA(repo *Repo) (string, bool) {
	sha, ok := repo.Refs["refs/heads/"+repo.DefaultBranch]
	return sha, ok
}

// sortedRefs returns [name, sha] pairs sorted by ref name, HEAD excluded.
func sortedRefs(repo *Repo) [][2]string {
	out := make([][2]string, 0, len(repo.Refs))
	for name, sha := range repo.Refs {
		out = append(out, [2]string{name, sha})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func v0Advertisement(repo *Repo, service string) []byte {
	var parts [][]byte
	serviceName := "git-upload-pack"
	if service == "receive" {
		serviceName = "git-receive-pack"
	}
	parts = append(parts, PktLine("# service="+serviceName+"\n"))
	parts = append(parts, PktFlush())
	caps := uploadCaps + " " + symrefCap(repo)
	if service == "receive" {
		caps = receiveCaps
	}
	head, hasHead := headSHA(repo)
	refs := sortedRefs(repo)
	if !hasHead && len(refs) == 0 {
		parts = append(parts, PktLine(ZERO_SHA+" capabilities^{}\x00"+caps+"\n"))
	} else {
		if hasHead {
			parts = append(parts, PktLine(head+" HEAD\x00"+caps+"\n"))
		} else {
			parts = append(parts, PktLine(ZERO_SHA+" capabilities^{}\x00"+caps+"\n"))
		}
		for _, ref := range refs {
			parts = append(parts, PktLine(ref[1]+" "+ref[0]+"\n"))
		}
	}
	parts = append(parts, PktFlush())
	return bytes.Join(parts, nil)
}

func v2Advertisement() []byte {
	var parts [][]byte
	parts = append(parts, PktLine("version 2\n"))
	parts = append(parts, PktLine("agent=forge/1.0\n"))
	parts = append(parts, PktLine("ls-refs=unborn\n"))
	parts = append(parts, PktLine("fetch=shallow\n"))
	parts = append(parts, PktLine("object-format=sha1\n"))
	parts = append(parts, PktFlush())
	return bytes.Join(parts, nil)
}

func gitNotFound() GitResponse {
	return GitResponse{
		Status:      404,
		ContentType: JSONType,
		Body:        mustMarshalJSON(obj{"message": "Not Found"}),
	}
}

// HandleInfoRefs answers GET .../info/refs. A nil repo answers 404.
func HandleInfoRefs(repo *Repo, service, gitProtocol string) GitResponse {
	if repo == nil {
		return gitNotFound()
	}
	if service == "git-upload-pack" && gitProtocol == "version=2" {
		return GitResponse{Status: 200, ContentType: "application/x-git-upload-pack-advertisement", Body: v2Advertisement(), ExtraHeaders: noCacheHeaders}
	}
	if service == "git-upload-pack" {
		return GitResponse{Status: 200, ContentType: "application/x-git-upload-pack-advertisement", Body: v0Advertisement(repo, "upload"), ExtraHeaders: noCacheHeaders}
	}
	if service == "git-receive-pack" {
		return GitResponse{Status: 200, ContentType: "application/x-git-receive-pack-advertisement", Body: v0Advertisement(repo, "receive"), ExtraHeaders: noCacheHeaders}
	}
	return gitNotFound()
}

// ---------------------------------------------------------------------------
// upload-pack
// ---------------------------------------------------------------------------

// FetchRequest is a parsed upload-pack request (v0 or v2 fetch args).
type FetchRequest struct {
	Wants    []string
	Haves    map[string]bool
	Shallows map[string]bool
	Deepen   *int
	Depth    *int
}

func newFetchRequest() FetchRequest {
	return FetchRequest{Haves: make(map[string]bool), Shallows: make(map[string]bool)}
}

// stripTrailingNewline removes a single trailing "\n", like JavaScript's
// replace(/\n$/, ”).
func stripTrailingNewline(s string) string {
	return strings.TrimSuffix(s, "\n")
}

func parseV0UploadRequest(body []byte) FetchRequest {
	lines, _, _ := ParsePktLines(body)
	req := newFetchRequest()
	for _, raw := range lines {
		line := stripTrailingNewline(raw)
		switch {
		case strings.HasPrefix(line, "want "):
			rest := line[5:]
			if nul := strings.IndexByte(rest, 0); nul != -1 {
				rest = rest[:nul]
			}
			req.Wants = append(req.Wants, strings.TrimSpace(rest))
		case strings.HasPrefix(line, "have "):
			req.Haves[strings.TrimSpace(line[5:])] = true
		case strings.HasPrefix(line, "deepen "):
			if n, err := strconv.Atoi(strings.TrimSpace(line[7:])); err == nil && n > 0 {
				req.Deepen = &n
			}
		case strings.HasPrefix(line, "shallow "):
			req.Shallows[strings.TrimSpace(line[8:])] = true
		}
	}
	return req
}

func optIntString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func packCacheKey(req FetchRequest, version int) string {
	wants := append([]string(nil), req.Wants...)
	sort.Strings(wants)
	return "v" + strconv.Itoa(version) + "|" + strings.Join(wants, ",") + "|" +
		strings.Join(sortedKeys(req.Haves), ",") + "|" +
		strings.Join(sortedKeys(req.Shallows), ",") + "|" +
		optIntString(req.Deepen) + "|" + optIntString(req.Depth)
}

// fetchPack runs the fetch walk (or serves the cached result) and returns
// the pack bytes plus shallow/unshallow bookkeeping. On a cache miss the
// build is serialized per repository so concurrent identical fetches
// share one build.
func fetchPack(repo *Repo, req FetchRequest) (*packCacheEntry, error) {
	key := packCacheKey(req, repo.Version)
	if v, ok := repo.packCache.Load(key); ok {
		return v.(*packCacheEntry), nil
	}
	repo.packMu.Lock()
	defer repo.packMu.Unlock()
	if v, ok := repo.packCache.Load(key); ok {
		return v.(*packCacheEntry), nil
	}
	walk := CollectForFetch(repo, req.Wants, req.Haves, FetchOptions{Depth: req.Depth, Deepen: req.Deepen, Shallows: req.Shallows})
	pack, err := WritePack(repo, walk.Objects)
	if err != nil {
		return nil, err
	}
	entry := &packCacheEntry{pack: pack, shallow: walk.Shallow, unshallow: walk.Unshallow}
	if repo.packCount.Load() > 64 {
		repo.packCache.Range(func(k, _ any) bool {
			repo.packCache.Delete(k)
			return true
		})
		repo.packCount.Store(0)
	}
	repo.packCache.Store(key, entry)
	repo.packCount.Add(1)
	return entry, nil
}

func gitInternalError(err error) GitResponse {
	return GitResponse{
		Status:      500,
		ContentType: JSONType,
		Body:        mustMarshalJSON(obj{"message": err.Error()}),
	}
}

// HandleUploadPackV0 answers a v0 upload-pack request body.
func HandleUploadPackV0(repo *Repo, body []byte) GitResponse {
	req := parseV0UploadRequest(body)
	entry, err := fetchPack(repo, req)
	if err != nil {
		return gitInternalError(err)
	}
	var parts [][]byte
	for _, sha := range entry.shallow {
		parts = append(parts, PktLine("shallow "+sha+"\n"))
	}
	for _, sha := range entry.unshallow {
		parts = append(parts, PktLine("unshallow "+sha+"\n"))
	}
	parts = append(parts, PktLine("NAK\n"))
	parts = append(parts, SidebandEncode(entry.pack, 1))
	return GitResponse{Status: 200, ContentType: "application/x-git-upload-pack-result", Body: bytes.Join(parts, nil), ExtraHeaders: noCacheHeaders}
}

// ---------------------------------------------------------------------------
// upload-pack v2
// ---------------------------------------------------------------------------

func handleLsRefs(repo *Repo, args []string) []byte {
	peel := false
	unborn := false
	symrefs := false
	var prefixes []string
	for _, raw := range args {
		line := stripTrailingNewline(raw)
		switch {
		case line == "peel":
			peel = true
		case line == "unborn":
			unborn = true
		case line == "symrefs":
			symrefs = true
		case strings.HasPrefix(line, "ref-prefix "):
			prefixes = append(prefixes, line[len("ref-prefix "):])
		case line == "ref-prefix":
			prefixes = append(prefixes, "")
		}
	}
	matches := func(name string) bool {
		if len(prefixes) == 0 {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
	var parts [][]byte
	head, hasHead := headSHA(repo)
	if hasHead && matches("HEAD") {
		line := head + " HEAD"
		if symrefs {
			line += " symref-target:refs/heads/" + repo.DefaultBranch
		}
		parts = append(parts, PktLine(line+"\n"))
	}
	for _, ref := range sortedRefs(repo) {
		if !matches(ref[0]) {
			continue
		}
		line := ref[1] + " " + ref[0]
		if peel {
			if GetTag(repo, ref[1]) != nil {
				line += " peeled:" + PeelToNonTag(repo, ref[1])
			}
		}
		parts = append(parts, PktLine(line+"\n"))
	}
	if !hasHead && unborn && matches("HEAD") {
		parts = append(parts, PktLine("unborn HEAD symref-target:refs/heads/"+repo.DefaultBranch+"\n"))
	}
	parts = append(parts, PktFlush())
	return bytes.Join(parts, nil)
}

func parseV2FetchArgs(args []string) FetchRequest {
	req := newFetchRequest()
	for _, raw := range args {
		line := stripTrailingNewline(raw)
		switch {
		case strings.HasPrefix(line, "want "):
			req.Wants = append(req.Wants, strings.TrimSpace(line[5:]))
		case strings.HasPrefix(line, "have "):
			req.Haves[strings.TrimSpace(line[5:])] = true
		case strings.HasPrefix(line, "deepen "):
			if n, err := strconv.Atoi(strings.TrimSpace(line[7:])); err == nil && n > 0 {
				req.Deepen = &n
			}
		case strings.HasPrefix(line, "depth "):
			if n, err := strconv.Atoi(strings.TrimSpace(line[6:])); err == nil && n > 0 {
				req.Depth = &n
			}
		case strings.HasPrefix(line, "shallow "):
			req.Shallows[strings.TrimSpace(line[8:])] = true
		}
		// filter / include-tag / ofs-delta / deepen-since / deepen-not:
		// accepted and intentionally unadvertised, so a client never
		// sends one that must be honored; sending the full object set
		// stays protocol-legal.
	}
	return req
}

func handleFetchV2(repo *Repo, args []string) ([]byte, error) {
	req := parseV2FetchArgs(args)
	entry, err := fetchPack(repo, req)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	if len(entry.shallow) > 0 || len(entry.unshallow) > 0 {
		// The shallow-info section: a header line, newline-less
		// shallow/unshallow lines, then the 0001 delimiter.
		parts = append(parts, PktLine("shallow-info\n"))
		for _, sha := range entry.shallow {
			parts = append(parts, PktLine("shallow "+sha))
		}
		for _, sha := range entry.unshallow {
			parts = append(parts, PktLine("unshallow "+sha))
		}
		parts = append(parts, []byte("0001"))
	}
	parts = append(parts, PktLine("packfile\n"))
	parts = append(parts, SidebandEncode(entry.pack, 1))
	return bytes.Join(parts, nil), nil
}

// HandleUploadPackV2 answers a protocol-v2 upload-pack request body.
func HandleUploadPackV2(repo *Repo, body []byte) GitResponse {
	lines, _, _ := ParsePktLines(body)
	first := ""
	if len(lines) > 0 {
		first = stripTrailingNewline(lines[0])
	}
	command := ""
	if strings.HasPrefix(first, "command=") {
		candidate := first[len("command="):]
		valid := candidate != ""
		for _, c := range candidate {
			if !(c >= 'a' && c <= 'z' || c == '-') {
				valid = false
				break
			}
		}
		if valid {
			command = candidate
		}
	}
	if command == "" {
		return GitResponse{
			Status:      200,
			ContentType: "application/x-git-upload-pack-result",
			Body:        bytes.Join([][]byte{PktLine("ERR unknown command\n"), PktFlush()}, nil),
		}
	}
	// Request-level lines (agent=, object-format=) precede the delimiter
	// and are not command arguments; drop them.
	var args []string
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "agent=") || strings.HasPrefix(l, "object-format=") {
			continue
		}
		args = append(args, l)
	}
	var out []byte
	switch command {
	case "ls-refs":
		out = handleLsRefs(repo, args)
	case "fetch":
		var err error
		out, err = handleFetchV2(repo, args)
		if err != nil {
			return gitInternalError(err)
		}
	default:
		out = bytes.Join([][]byte{PktLine("ERR unknown command " + command + "\n"), PktFlush()}, nil)
	}
	return GitResponse{Status: 200, ContentType: "application/x-git-upload-pack-result", Body: out}
}

// ---------------------------------------------------------------------------
// receive-pack v0
// ---------------------------------------------------------------------------

// RefUpdate is one ref command in a receive-pack request.
type RefUpdate struct {
	OldSHA string
	NewSHA string
	Ref    string
}

// parseRefCommand parses "old new ref" with exactly one space between
// fields and lowercase-hex 40-char shas, mirroring the reference regex
// ^([0-9a-f]{40}) ([0-9a-f]{40}) (\S+)$.
func parseRefCommand(head string) (RefUpdate, bool) {
	if len(head) < 83 || head[40] != ' ' || head[81] != ' ' {
		return RefUpdate{}, false
	}
	isHex := func(s string) bool {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	oldSHA, newSHA, ref := head[:40], head[41:81], head[82:]
	if !isHex(oldSHA) || !isHex(newSHA) {
		return RefUpdate{}, false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			return RefUpdate{}, false
		}
	}
	return RefUpdate{OldSHA: oldSHA, NewSHA: newSHA, Ref: ref}, true
}

func parseReceiveRequest(body []byte) (updates []RefUpdate, packStart int, wantSideband bool) {
	lines, consumed, _ := ParsePktLines(body)
	for i, rawLine := range lines {
		raw := stripTrailingNewline(rawLine)
		if strings.HasPrefix(raw, "push-option ") {
			continue
		}
		head := raw
		if nul := strings.IndexByte(raw, 0); nul != -1 {
			head = raw[:nul]
			if i == 0 {
				caps := raw[nul+1:]
				if strings.Contains(caps, "side-band-64k") || strings.Contains(caps, "side-band") {
					wantSideband = true
				}
			}
		}
		if upd, ok := parseRefCommand(head); ok {
			updates = append(updates, upd)
		}
	}
	return updates, consumed, wantSideband
}

// storePackObject stores a decoded pack object and seeds the deflate
// cache with its original compressed form when one was captured, so a
// later fetch re-emits the object without re-deflating.
func storePackObject(repo *Repo, obj PackObject) {
	StoreObject(repo, obj.Type, obj.Data)
	if obj.Raw != nil {
		if _, ok := repo.deflateCache.Load(obj.SHA); !ok {
			repo.deflateCache.Store(obj.SHA, obj.Raw)
			repo.deflateCount.Add(1)
		}
	}
}

// HandleReceivePack answers a v0 receive-pack request body: unpack the
// pack, apply fast-forward-checked ref updates, and report.
func HandleReceivePack(repo *Repo, body []byte) GitResponse {
	updates, packStart, wantSideband := parseReceiveRequest(body)
	type result struct {
		ref    string
		ok     bool
		reason string
	}
	var results []result
	unpackError := ""

	needsPack := false
	for _, u := range updates {
		if !IsZeroSHA(u.NewSHA) {
			needsPack = true
			break
		}
	}
	if needsPack {
		packBytes := body[packStart:]
		if len(packBytes) == 0 {
			unpackError = "empty packfile"
		} else {
			objects, err := ReadPack(packBytes, func(sha string) *GitObject {
				return repo.Objects[sha]
			})
			if err != nil {
				unpackError = err.Error()
			} else {
				for _, obj := range objects {
					storePackObject(repo, obj)
				}
			}
		}
	}

	if unpackError != "" {
		for _, u := range updates {
			results = append(results, result{ref: u.Ref, ok: false, reason: "unpacker error"})
		}
	} else {
		for _, u := range updates {
			current, exists := repo.Refs[u.Ref]
			if IsZeroSHA(u.NewSHA) {
				// Delete.
				if !exists {
					results = append(results, result{ref: u.Ref, ok: false, reason: "remote ref does not exist"})
				} else if !IsZeroSHA(u.OldSHA) && u.OldSHA != current {
					results = append(results, result{ref: u.Ref, ok: false, reason: "non-fast-forward"})
				} else {
					delete(repo.Refs, u.Ref)
					results = append(results, result{ref: u.Ref, ok: true})
				}
				continue
			}
			target := repo.Objects[u.NewSHA]
			if target == nil {
				results = append(results, result{ref: u.Ref, ok: false, reason: "object not found"})
				continue
			}
			newCommit := ResolveToCommit(repo, u.NewSHA)
			if newCommit == "" && target.Type != ObjectTag {
				results = append(results, result{ref: u.Ref, ok: false, reason: "object not found"})
				continue
			}
			if !exists {
				if !IsZeroSHA(u.OldSHA) {
					results = append(results, result{ref: u.Ref, ok: false, reason: "non-fast-forward"})
					continue
				}
				repo.Refs[u.Ref] = u.NewSHA
				results = append(results, result{ref: u.Ref, ok: true})
				continue
			}
			if u.OldSHA != current {
				results = append(results, result{ref: u.Ref, ok: false, reason: "non-fast-forward"})
				continue
			}
			if u.NewSHA == current {
				results = append(results, result{ref: u.Ref, ok: true})
				continue
			}
			oldCommit := ResolveToCommit(repo, current)
			if oldCommit == "" || newCommit == "" || !IsAncestor(repo, oldCommit, newCommit) {
				results = append(results, result{ref: u.Ref, ok: false, reason: "non-fast-forward"})
				continue
			}
			repo.Refs[u.Ref] = u.NewSHA
			results = append(results, result{ref: u.Ref, ok: true})
		}
	}

	anyOK := false
	for _, r := range results {
		if r.ok {
			anyOK = true
			break
		}
	}
	if anyOK {
		repo.Touch()
	}

	var report strings.Builder
	if unpackError == "" {
		report.WriteString("unpack ok\n")
	} else {
		report.WriteString("unpack " + unpackError + "\n")
	}
	for _, r := range results {
		if r.ok {
			report.WriteString("ok " + r.ref + "\n")
		} else {
			report.WriteString("ng " + r.ref + " " + r.reason + "\n")
		}
	}
	reportBuf := []byte(report.String())
	var out []byte
	if wantSideband {
		out = SidebandEncode(reportBuf, 1)
	} else {
		var parts [][]byte
		for _, l := range strings.Split(report.String(), "\n") {
			if l != "" {
				parts = append(parts, PktLine(l+"\n"))
			}
		}
		parts = append(parts, PktFlush())
		out = bytes.Join(parts, nil)
	}
	return GitResponse{Status: 200, ContentType: "application/x-git-receive-pack-result", Body: out, ExtraHeaders: noCacheHeaders}
}
