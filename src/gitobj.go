// Git object model and pack codec: hashing, commit/tree/tag parsing, tree
// building, packfile reading (including ofs/ref deltas) and writing,
// reachability traversal, pkt-line framing and sideband framing. Pure
// functions over the store; no I/O. Callers must hold Store.mu.RLock (or
// Lock) while calling these functions, because they read Repo maps; the
// sync.Map caches are safe under either lock.
package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/adler32"
	"io"
	"sort"
	"strings"
)

// HashObject returns the git object id for type+data.
func HashObject(typ ObjectType, data []byte) string {
	h := sha1.New()
	h.Write([]byte(fmt.Sprintf("%s %d\x00", typ, len(data))))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// StoreObject stores data as an object of the given type and returns its
// sha. Existing objects are kept as-is; object data is immutable.
func StoreObject(repo *Repo, typ ObjectType, data []byte) string {
	sha := HashObject(typ, data)
	if _, ok := repo.Objects[sha]; !ok {
		repo.Objects[sha] = &GitObject{Type: typ, Data: data}
	}
	return sha
}

// ParseCommit parses a commit object's raw bytes.
func ParseCommit(data []byte) *CommitInfo {
	text := string(data)
	headEnd := strings.Index(text, "\n\n")
	head := text
	message := ""
	if headEnd != -1 {
		head = text[:headEnd]
		message = text[headEnd+2:]
	}
	info := &CommitInfo{}
	for _, line := range strings.Split(head, "\n") {
		switch {
		case strings.HasPrefix(line, "tree "):
			info.Tree = strings.TrimSpace(line[5:])
		case strings.HasPrefix(line, "parent "):
			info.Parents = append(info.Parents, strings.TrimSpace(line[7:]))
		case strings.HasPrefix(line, "author "):
			info.Author = line[7:]
		case strings.HasPrefix(line, "committer "):
			info.Committer = line[10:]
		}
	}
	info.Message = message
	return info
}

// GetCommit returns the parsed commit for sha, or nil when sha is not a
// stored commit. Results are cached; commit objects are immutable.
func GetCommit(repo *Repo, sha string) *CommitInfo {
	if v, ok := repo.commitCache.Load(sha); ok {
		return v.(*CommitInfo)
	}
	obj := repo.Objects[sha]
	if obj == nil || obj.Type != ObjectCommit {
		return nil
	}
	info := ParseCommit(obj.Data)
	repo.commitCache.Store(sha, info)
	return info
}

// ParseTree parses a tree object's raw bytes.
func ParseTree(data []byte) []TreeEntry {
	var entries []TreeEntry
	pos := 0
	for pos < len(data) {
		sp := bytes.IndexByte(data[pos:], ' ')
		if sp == -1 {
			break
		}
		sp += pos
		mode := string(data[pos:sp])
		nul := bytes.IndexByte(data[sp+1:], 0)
		if nul == -1 {
			break
		}
		nul += sp + 1
		name := string(data[sp+1 : nul])
		if nul+21 > len(data) {
			break
		}
		sha := hex.EncodeToString(data[nul+1 : nul+21])
		kind := "blob"
		switch mode {
		case "40000":
			kind = "tree"
		case "160000":
			kind = "commit"
		}
		entries = append(entries, TreeEntry{Mode: mode, Name: name, SHA: sha, Kind: kind})
		pos = nul + 21
	}
	return entries
}

// GetTree returns the parsed tree for sha, or nil when sha is not a
// stored tree.
func GetTree(repo *Repo, sha string) []TreeEntry {
	if v, ok := repo.treeCache.Load(sha); ok {
		return v.([]TreeEntry)
	}
	obj := repo.Objects[sha]
	if obj == nil || obj.Type != ObjectTree {
		return nil
	}
	entries := ParseTree(obj.Data)
	repo.treeCache.Store(sha, entries)
	return entries
}

// ParseTag parses an annotated tag object's raw bytes.
func ParseTag(data []byte) *TagInfo {
	text := string(data)
	headEnd := strings.Index(text, "\n\n")
	head := text
	message := ""
	if headEnd != -1 {
		head = text[:headEnd]
		message = text[headEnd+2:]
	}
	info := &TagInfo{}
	for _, line := range strings.Split(head, "\n") {
		switch {
		case strings.HasPrefix(line, "object "):
			info.Object = strings.TrimSpace(line[7:])
		case strings.HasPrefix(line, "type "):
			info.Type = strings.TrimSpace(line[5:])
		case strings.HasPrefix(line, "tag "):
			info.Tag = line[4:]
		case strings.HasPrefix(line, "tagger "):
			info.Tagger = line[7:]
		}
	}
	info.Message = message
	return info
}

// GetTag returns the parsed tag for sha, or nil when sha is not a stored
// tag object.
func GetTag(repo *Repo, sha string) *TagInfo {
	if v, ok := repo.tagCache.Load(sha); ok {
		return v.(*TagInfo)
	}
	obj := repo.Objects[sha]
	if obj == nil || obj.Type != ObjectTag {
		return nil
	}
	info := ParseTag(obj.Data)
	repo.tagCache.Store(sha, info)
	return info
}

// PeelToNonTag peels a tag (recursively) to the underlying non-tag object
// sha. Cycles terminate on the repeated sha.
func PeelToNonTag(repo *Repo, sha string) string {
	cur := sha
	seen := make(map[string]bool)
	for {
		if seen[cur] {
			return cur
		}
		seen[cur] = true
		tag := GetTag(repo, cur)
		if tag == nil {
			return cur
		}
		cur = tag.Object
	}
}

// ResolveToCommit resolves a sha to a commit sha: tags are peeled; any
// other non-commit object resolves to "".
func ResolveToCommit(repo *Repo, sha string) string {
	peeled := PeelToNonTag(repo, sha)
	if GetCommit(repo, peeled) == nil {
		return ""
	}
	return peeled
}

// treeSortKey returns the comparison key for a tree entry name: directory
// names compare as if suffixed with '/'.
func treeSortKey(e TreeEntry) string {
	if e.Kind == "tree" {
		return e.Name + "/"
	}
	return e.Name
}

// BuildTree stores a tree from entries (sorted git-style) and returns its
// sha.
func BuildTree(repo *Repo, entries []TreeEntry) string {
	sorted := make([]TreeEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return treeSortKey(sorted[i]) < treeSortKey(sorted[j])
	})
	var buf bytes.Buffer
	for _, e := range sorted {
		fmt.Fprintf(&buf, "%s %s\x00", e.Mode, e.Name)
		raw, err := hex.DecodeString(e.SHA)
		if err != nil || len(raw) != 20 {
			// A malformed sha cannot occur for stored objects; keep the
			// tree well-formed by writing a zero id rather than failing
			// the whole operation on corrupt input.
			raw = make([]byte, 20)
		}
		buf.Write(raw)
	}
	return StoreObject(repo, ObjectTree, buf.Bytes())
}

// BuildCommit stores a commit and returns its sha.
func BuildCommit(repo *Repo, tree string, parents []string, author, committer, message string) string {
	var sb strings.Builder
	sb.WriteString("tree " + tree + "\n")
	for _, p := range parents {
		sb.WriteString("parent " + p + "\n")
	}
	sb.WriteString("author " + author + "\n")
	sb.WriteString("committer " + committer + "\n\n")
	sb.WriteString(message)
	return StoreObject(repo, ObjectCommit, []byte(sb.String()))
}

// FlattenTree flattens a tree (recursively) into path -> entry. The
// returned map is cached and shared: callers must copy it before
// mutating. Missing trees flatten to an empty map.
func FlattenTree(repo *Repo, treeSHA string) map[string]FlatEntry {
	if v, ok := repo.flatCache.Load(treeSHA); ok {
		return v.(map[string]FlatEntry)
	}
	out := make(map[string]FlatEntry)
	var walk func(sha, prefix string)
	walk = func(sha, prefix string) {
		entries := GetTree(repo, sha)
		if entries == nil {
			return
		}
		for _, e := range entries {
			path := e.Name
			if prefix != "" {
				path = prefix + "/" + e.Name
			}
			if e.Kind == "tree" {
				walk(e.SHA, path)
			} else {
				out[path] = FlatEntry{SHA: e.SHA, Mode: e.Mode}
			}
		}
	}
	walk(treeSHA, "")
	repo.flatCache.Store(treeSHA, out)
	return out
}

// TreeFromFlat builds nested trees from a flat path map and returns the
// root tree sha.
func TreeFromFlat(repo *Repo, flat map[string]FlatEntry) string {
	type node struct {
		files map[string]FlatEntry
		dirs  map[string]*node
	}
	newNode := func() *node {
		return &node{files: make(map[string]FlatEntry), dirs: make(map[string]*node)}
	}
	root := newNode()
	for path, entry := range flat {
		parts := strings.Split(path, "/")
		cur := root
		for i := 0; i < len(parts)-1; i++ {
			next := cur.dirs[parts[i]]
			if next == nil {
				next = newNode()
				cur.dirs[parts[i]] = next
			}
			cur = next
		}
		cur.files[parts[len(parts)-1]] = entry
	}
	var build func(n *node) string
	build = func(n *node) string {
		var entries []TreeEntry
		for name, entry := range n.files {
			kind := "blob"
			if entry.Mode == "160000" {
				kind = "commit"
			}
			entries = append(entries, TreeEntry{Mode: entry.Mode, Name: name, SHA: entry.SHA, Kind: kind})
		}
		for name, child := range n.dirs {
			entries = append(entries, TreeEntry{Mode: "40000", Name: name, SHA: build(child), Kind: "tree"})
		}
		return BuildTree(repo, entries)
	}
	return build(root)
}

// IsAncestor reports whether ancestor is reachable from tip following
// parent links (or equal to tip).
func IsAncestor(repo *Repo, ancestor, tip string) bool {
	if ancestor == tip {
		return true
	}
	seen := make(map[string]bool)
	stack := []string{tip}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == ancestor {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		commit := GetCommit(repo, cur)
		if commit == nil {
			continue
		}
		stack = append(stack, commit.Parents...)
	}
	return false
}

// MergeBase returns the first common ancestor of two commits: BFS from
// both sides, scored by the sum of distances, first minimum wins.
func MergeBase(repo *Repo, a, b string) string {
	if a == b {
		return a
	}
	distA := make(map[string]int)
	queueA := []string{a}
	distA[a] = 0
	for i := 0; i < len(queueA); i++ {
		cur := queueA[i]
		commit := GetCommit(repo, cur)
		if commit == nil {
			continue
		}
		for _, p := range commit.Parents {
			if _, ok := distA[p]; !ok {
				distA[p] = distA[cur] + 1
				queueA = append(queueA, p)
			}
		}
	}
	seenB := map[string]bool{b: true}
	queueB := []string{b}
	best := ""
	bestScore := int(^uint(0) >> 1) // max int
	for i := 0; i < len(queueB); i++ {
		cur := queueB[i]
		if da, ok := distA[cur]; ok {
			if score := da + i; score < bestScore {
				bestScore = score
				best = cur
			}
		}
		commit := GetCommit(repo, cur)
		if commit == nil {
			continue
		}
		for _, p := range commit.Parents {
			if !seenB[p] {
				seenB[p] = true
				queueB = append(queueB, p)
			}
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Deltas and packfiles
// ---------------------------------------------------------------------------

// maxDeltaTarget bounds the output of a single delta application. Legit
// harness blobs are a few KiB; the bound only stops corrupt deltas from
// forcing unbounded allocation.
const maxDeltaTarget = 256 << 20

// readDeltaVarint reads a delta size varint: little-endian 7-bit groups,
// MSB set on all but the last byte.
func readDeltaVarint(data []byte, pos int) (value int, next int, err error) {
	value = 0
	shift := uint(0)
	p := pos
	for {
		if p >= len(data) {
			return 0, 0, errors.New("delta varint overruns buffer")
		}
		b := data[p]
		p++
		if shift >= 63 && b&0x7f != 0 {
			return 0, 0, errors.New("delta varint overflows")
		}
		value |= int(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
	}
	return value, p, nil
}

// ApplyDelta applies a git delta to base and returns the result.
func ApplyDelta(base, delta []byte) ([]byte, error) {
	pos := 0
	srcSize, next, err := readDeltaVarint(delta, pos)
	if err != nil {
		return nil, err
	}
	pos = next
	if srcSize != len(base) {
		return nil, errors.New("delta source size mismatch")
	}
	tgtSize, next, err := readDeltaVarint(delta, pos)
	if err != nil {
		return nil, err
	}
	pos = next
	if tgtSize < 0 || tgtSize > maxDeltaTarget {
		return nil, errors.New("delta target size out of range")
	}
	out := make([]byte, 0, tgtSize)
	for pos < len(delta) {
		op := delta[pos]
		pos++
		if op&0x80 != 0 {
			offset := 0
			size := 0
			if op&0x01 != 0 {
				if pos >= len(delta) {
					return nil, errors.New("delta copy overruns buffer")
				}
				offset = int(delta[pos])
				pos++
			}
			if op&0x02 != 0 {
				if pos >= len(delta) {
					return nil, errors.New("delta copy overruns buffer")
				}
				offset |= int(delta[pos]) << 8
				pos++
			}
			if op&0x04 != 0 {
				if pos >= len(delta) {
					return nil, errors.New("delta copy overruns buffer")
				}
				offset |= int(delta[pos]) << 16
				pos++
			}
			if op&0x08 != 0 {
				if pos >= len(delta) {
					return nil, errors.New("delta copy overruns buffer")
				}
				offset |= int(delta[pos]) << 24
				pos++
			}
			if op&0x10 != 0 {
				if pos >= len(delta) {
					return nil, errors.New("delta copy overruns buffer")
				}
				size = int(delta[pos])
				pos++
			}
			if op&0x20 != 0 {
				if pos >= len(delta) {
					return nil, errors.New("delta copy overruns buffer")
				}
				size |= int(delta[pos]) << 8
				pos++
			}
			if op&0x40 != 0 {
				if pos >= len(delta) {
					return nil, errors.New("delta copy overruns buffer")
				}
				size |= int(delta[pos]) << 16
				pos++
			}
			if size == 0 {
				size = 0x10000
			}
			if offset < 0 || size < 0 || offset+size > len(base) {
				return nil, errors.New("delta copy out of range")
			}
			out = append(out, base[offset:offset+size]...)
		} else if op != 0 {
			n := int(op)
			if pos+n > len(delta) {
				return nil, errors.New("delta insert out of range")
			}
			out = append(out, delta[pos:pos+n]...)
			pos += n
		} else {
			return nil, errors.New("delta opcode 0")
		}
	}
	if len(out) != tgtSize {
		return nil, errors.New("delta target size mismatch")
	}
	return out, nil
}

var packTypeNames = map[int]ObjectType{1: ObjectCommit, 2: ObjectTree, 3: ObjectBlob, 4: ObjectTag}
var packTypeNums = map[ObjectType]int{ObjectCommit: 1, ObjectTree: 2, ObjectBlob: 3, ObjectTag: 4}

// countingReader is a byte reader over a slice that records how many
// bytes were consumed. compress/flate reads exactly the bytes a DEFLATE
// stream needs when its input implements io.ByteReader, so the consumed
// count is the stream's exact end.
type countingReader struct {
	data []byte
	pos  int
}

func (r *countingReader) ReadByte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// inflateAt inflates the single zlib stream starting at start in data,
// verifying its declared output size and Adler-32 checksum. It returns
// the inflated bytes and the offset just past the stream (including the
// 2-byte zlib header and 4-byte checksum). Exactly one inflate pass is
// performed per stream.
func inflateAt(data []byte, start, expectedSize int) (out []byte, next int, err error) {
	if start+2 > len(data) {
		return nil, 0, errors.New("pack object overruns buffer")
	}
	// Validate the zlib header the way a zlib decoder does: compression
	// method 8, header checksum multiple of 31, no preset dictionary.
	cmf := data[start]
	flg := data[start+1]
	if cmf&0x0f != 8 || (int(cmf)*256+int(flg))%31 != 0 || flg&0x20 != 0 {
		return nil, 0, errors.New("bad zlib header in pack object")
	}
	cr := &countingReader{data: data[start+2:]}
	fr := flate.NewReader(cr)
	defer fr.Close()
	out, err = io.ReadAll(fr)
	if err != nil {
		return nil, 0, fmt.Errorf("pack object inflate failed: %w", err)
	}
	if len(out) != expectedSize {
		return nil, 0, errors.New("pack object size mismatch")
	}
	adlerStart := start + 2 + cr.pos
	if adlerStart+4 > len(data) {
		return nil, 0, errors.New("pack object checksum overruns buffer")
	}
	if binary.BigEndian.Uint32(data[adlerStart:adlerStart+4]) != adler32.Checksum(out) {
		return nil, 0, errors.New("pack object checksum mismatch")
	}
	return out, adlerStart + 4, nil
}

// PackObject is one object decoded from a packfile. Raw, when set, is the
// object's verbatim compressed form (pack header + zlib stream) as read
// from the pack, reusable when re-emitting the object without
// re-deflating. Delta-resolved objects have no Raw.
type PackObject struct {
	Type ObjectType
	Data []byte
	SHA  string
	Raw  []byte
}

// ReadPack parses a packfile buffer into objects with deltas resolved.
// resolveBase looks up a base object already in the store (for ref
// deltas); it may return nil. Malformed input returns a descriptive
// error; nothing is stored by this function.
func ReadPack(pack []byte, resolveBase func(sha string) *GitObject) ([]PackObject, error) {
	if len(pack) < 12 {
		return nil, errors.New("pack too short")
	}
	if string(pack[:4]) != "PACK" {
		return nil, errors.New("bad pack signature")
	}
	version := binary.BigEndian.Uint32(pack[4:8])
	if version != 2 && version != 3 {
		return nil, errors.New("unsupported pack version")
	}
	count := binary.BigEndian.Uint32(pack[8:12])
	// Verify the trailer when the buffer holds at least a full pack.
	if len(pack) >= 32 {
		body := pack[:len(pack)-20]
		trailer := hex.EncodeToString(pack[len(pack)-20:])
		digest := sha1.Sum(body)
		if hex.EncodeToString(digest[:]) != trailer {
			return nil, errors.New("pack trailer checksum mismatch")
		}
	}
	objects := make([]PackObject, 0, count)
	// Indices (not pointers) into objects: append may reallocate the
	// slice, which would invalidate stored pointers.
	byOffset := make(map[int]int)
	bySHA := make(map[string]int)
	pos := 12
	for i := uint32(0); i < count; i++ {
		start := pos
		if pos >= len(pack) {
			return nil, errors.New("pack object header overruns buffer")
		}
		b := pack[pos]
		pos++
		typeNum := int((b >> 4) & 0x07)
		size := int(b & 0x0f)
		shift := uint(4)
		for b&0x80 != 0 {
			if pos >= len(pack) {
				return nil, errors.New("pack object header overruns buffer")
			}
			b = pack[pos]
			pos++
			if shift >= 63 && b&0x7f != 0 {
				return nil, errors.New("pack object size overflows")
			}
			size |= int(b&0x7f) << shift
			shift += 7
		}
		switch typeNum {
		case 6: // OFS_DELTA
			if pos >= len(pack) {
				return nil, errors.New("ofs-delta offset overruns buffer")
			}
			offByte := pack[pos]
			pos++
			offset := int(offByte & 0x7f)
			for offByte&0x80 != 0 {
				if pos >= len(pack) {
					return nil, errors.New("ofs-delta offset overruns buffer")
				}
				offByte = pack[pos]
				pos++
				offset = ((offset + 1) << 7) | int(offByte&0x7f)
			}
			baseIdx, ok := byOffset[start-offset]
			if !ok {
				return nil, errors.New("ofs-delta base not found")
			}
			base := objects[baseIdx]
			delta, next, err := inflateAt(pack, pos, size)
			if err != nil {
				return nil, err
			}
			pos = next
			data, err := ApplyDelta(base.Data, delta)
			if err != nil {
				return nil, err
			}
			obj := PackObject{Type: base.Type, Data: data, SHA: HashObject(base.Type, data)}
			objects = append(objects, obj)
			byOffset[start] = len(objects) - 1
			bySHA[obj.SHA] = len(objects) - 1
		case 7: // REF_DELTA
			if pos+20 > len(pack) {
				return nil, errors.New("ref-delta base sha overruns buffer")
			}
			baseSHA := hex.EncodeToString(pack[pos : pos+20])
			pos += 20
			delta, next, err := inflateAt(pack, pos, size)
			if err != nil {
				return nil, err
			}
			pos = next
			var baseData []byte
			var baseType ObjectType
			if inPackIdx, ok := bySHA[baseSHA]; ok {
				inPack := objects[inPackIdx]
				baseData, baseType = inPack.Data, inPack.Type
			} else if resolveBase != nil {
				if stored := resolveBase(baseSHA); stored != nil {
					baseData, baseType = stored.Data, stored.Type
				}
			}
			if baseData == nil {
				return nil, errors.New("ref-delta base not found")
			}
			data, err := ApplyDelta(baseData, delta)
			if err != nil {
				return nil, err
			}
			obj := PackObject{Type: baseType, Data: data, SHA: HashObject(baseType, data)}
			objects = append(objects, obj)
			byOffset[start] = len(objects) - 1
			bySHA[obj.SHA] = len(objects) - 1
		default:
			typ, ok := packTypeNames[typeNum]
			if !ok {
				return nil, fmt.Errorf("unknown pack object type %d", typeNum)
			}
			out, next, err := inflateAt(pack, pos, size)
			if err != nil {
				return nil, err
			}
			raw := pack[start:next]
			pos = next
			obj := PackObject{Type: typ, Data: out, SHA: HashObject(typ, out), Raw: raw}
			objects = append(objects, obj)
			byOffset[start] = len(objects) - 1
			bySHA[obj.SHA] = len(objects) - 1
		}
	}
	return objects, nil
}

// packObjectHeader encodes a pack object header for a type and size.
func packObjectHeader(typeNum, size int) []byte {
	var out []byte
	first := (typeNum << 4) | (size & 0x0f)
	rest := size >> 4
	if rest != 0 {
		first |= 0x80
	}
	out = append(out, byte(first))
	for rest != 0 {
		b := rest & 0x7f
		rest >>= 7
		if rest != 0 {
			b |= 0x80
		}
		out = append(out, byte(b))
	}
	return out
}

// deflateLevel1 returns the zlib stream for data at the fastest
// compression level. Pack payloads are checksummed by the pack trailer,
// so any valid zlib stream is acceptable; level 1 keeps fetch latency
// low.
func deflateLevel1(data []byte) ([]byte, error) {
	// A zlib stream is a 2-byte header, the raw DEFLATE stream, and the
	// Adler-32 checksum. Emit it directly so the deflate level is
	// explicit and no extra buffering is involved.
	var buf bytes.Buffer
	header := []byte{0x78, 0x01} // CMF/FLG for level 1, FCHECK valid.
	buf.Write(header)
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], adler32.Checksum(data))
	buf.Write(sum[:])
	return buf.Bytes(), nil
}

// deflatedPayload returns the cached pack form (header + zlib stream) for
// an object, computing and caching it on a miss. Raw forms captured from
// incoming packs are reused verbatim when present.
func deflatedPayload(repo *Repo, obj PackObject) ([]byte, error) {
	if v, ok := repo.deflateCache.Load(obj.SHA); ok {
		return v.([]byte), nil
	}
	var payload []byte
	if obj.Raw != nil {
		payload = obj.Raw
	} else {
		stream, err := deflateLevel1(obj.Data)
		if err != nil {
			return nil, err
		}
		payload = append(packObjectHeader(packTypeNums[obj.Type], len(obj.Data)), stream...)
	}
	if repo.deflateCount.Load() > 4096 {
		repo.deflateCache.Range(func(k, _ any) bool {
			repo.deflateCache.Delete(k)
			return true
		})
		repo.deflateCount.Store(0)
	}
	repo.deflateCache.Store(obj.SHA, payload)
	repo.deflateCount.Add(1)
	return payload, nil
}

// WritePack serializes objects (no deltas) into a packfile. Deflated
// payloads are cached per repository; objects that arrived in a pack
// reuse their original compressed bytes.
func WritePack(repo *Repo, objects []PackObject) ([]byte, error) {
	var body bytes.Buffer
	header := make([]byte, 12)
	copy(header, "PACK")
	binary.BigEndian.PutUint32(header[4:8], 2)
	binary.BigEndian.PutUint32(header[8:12], uint32(len(objects)))
	body.Write(header)
	for _, obj := range objects {
		payload, err := deflatedPayload(repo, obj)
		if err != nil {
			return nil, err
		}
		body.Write(payload)
	}
	digest := sha1.Sum(body.Bytes())
	out := body.Bytes()
	out = append(out, digest[:]...)
	return out, nil
}

// ---------------------------------------------------------------------------
// Reachability traversal for upload-pack
// ---------------------------------------------------------------------------

// WalkResult is the outcome of a fetch walk: the objects to send, in
// deterministic emission order, plus shallow/unshallow bookkeeping.
type WalkResult struct {
	Objects   []PackObject
	Shallow   []string
	Unshallow []string
}

// FetchOptions carries the depth controls of a fetch request.
type FetchOptions struct {
	// Depth is a fresh depth limit: include commits with distance <
	// Depth from the nearest want. Nil means no fresh limit.
	Depth *int
	// Deepen extends the client's current shallow boundary by N.
	Deepen *int
	// Shallows are the commits the client declares as its shallow
	// boundary.
	Shallows map[string]bool
}

// CollectForFetch collects the objects for a fetch: everything reachable
// from wants (following tag targets and commit parents), minus objects
// the client already has, honoring depth/deepen limits and
// shallow-boundary bookkeeping.
func CollectForFetch(repo *Repo, wants []string, haves map[string]bool, opts FetchOptions) WalkResult {
	// Distance from the nearest want, over commit parents (tags peel to
	// commits).
	dist := make(map[string]int)
	var queue []string
	for _, want := range wants {
		peeled := PeelToNonTag(repo, want)
		if GetCommit(repo, peeled) != nil {
			if _, ok := dist[peeled]; !ok {
				dist[peeled] = 0
				queue = append(queue, peeled)
			}
		}
	}
	for i := 0; i < len(queue); i++ {
		cur := queue[i]
		d := dist[cur]
		commit := GetCommit(repo, cur)
		if commit == nil {
			continue
		}
		for _, p := range commit.Parents {
			if _, ok := dist[p]; !ok {
				dist[p] = d + 1
				queue = append(queue, p)
			}
		}
	}
	// What the client already holds, split by kind: a plain have means
	// the commit and all its ancestors; a have that is also a declared
	// shallow boundary commit means that commit object only.
	shallows := opts.Shallows
	if shallows == nil {
		shallows = make(map[string]bool)
	}
	shallowPeeled := make(map[string]bool)
	for s := range shallows {
		shallowPeeled[PeelToNonTag(repo, s)] = true
	}
	fullyHad := make(map[string]bool)
	{
		var stack []string
		for h := range haves {
			peeled := PeelToNonTag(repo, h)
			if !shallowPeeled[peeled] {
				stack = append(stack, peeled)
			}
		}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if fullyHad[cur] {
				continue
			}
			fullyHad[cur] = true
			if commit := GetCommit(repo, cur); commit != nil {
				stack = append(stack, commit.Parents...)
			}
		}
	}
	clientHas := make(map[string]bool, len(fullyHad))
	for k := range fullyHad {
		clientHas[k] = true
	}
	for h := range haves {
		peeled := PeelToNonTag(repo, h)
		if shallowPeeled[peeled] {
			clientHas[peeled] = true
		}
	}
	// Effective depth limit: a fresh Depth counts from the wants; a
	// Deepen extends from the client's declared shallow boundary (limit
	// = deepen + distance of the deepest reachable boundary commit).
	var depthLimit *int
	if opts.Depth != nil {
		depthLimit = opts.Depth
	} else if opts.Deepen != nil {
		base := 0
		for s := range shallowPeeled {
			if d, ok := dist[s]; ok && d > base {
				base = d
			}
		}
		limit := *opts.Deepen + base
		depthLimit = &limit
	}
	included := make(map[string]bool)
	for sha, d := range dist {
		if clientHas[sha] {
			continue
		}
		if depthLimit == nil || d < *depthLimit {
			included[sha] = true
		}
	}

	var shallow, unshallow []string
	if depthLimit != nil {
		for sha := range included {
			commit := GetCommit(repo, sha)
			if commit == nil {
				continue
			}
			cut := false
			for _, p := range commit.Parents {
				if !included[p] && !fullyHad[p] {
					cut = true
					break
				}
			}
			if cut && len(commit.Parents) > 0 {
				shallow = append(shallow, sha)
			}
		}
		if len(included) == 0 {
			// Nothing new, but the client asked to deepen from a
			// boundary it holds: re-declare the held wants as the
			// shallow boundary.
			for _, want := range wants {
				peeled := PeelToNonTag(repo, want)
				if haves[want] || haves[peeled] {
					shallow = append(shallow, peeled)
				}
			}
		}
	}
	// Unshallow: a declared boundary commit whose parents are now all
	// included or fully held (fixpoint, since boundary commits chain).
	{
		unshallowSet := make(map[string]bool)
		changed := true
		for changed {
			changed = false
			for s := range shallowPeeled {
				if _, ok := dist[s]; !ok || unshallowSet[s] {
					continue
				}
				commit := GetCommit(repo, s)
				if commit == nil || len(commit.Parents) == 0 {
					continue
				}
				all := true
				for _, p := range commit.Parents {
					if !included[p] && !fullyHad[p] && !unshallowSet[p] {
						all = false
						break
					}
				}
				if all {
					unshallowSet[s] = true
					unshallow = append(unshallow, s)
					changed = true
				}
			}
		}
	}
	sort.Strings(shallow)
	sort.Strings(unshallow)

	// Gather trees and blobs for included commits, minus the haves'
	// closure.
	haveObjects := make(map[string]bool)
	{
		var stack []string
		for h := range haves {
			peeled := PeelToNonTag(repo, h)
			if commit := GetCommit(repo, peeled); commit != nil {
				stack = append(stack, commit.Tree)
			}
			if obj := repo.Objects[peeled]; obj != nil && obj.Type != ObjectCommit {
				stack = append(stack, peeled)
			}
		}
		seenTrees := make(map[string]bool)
		drain := func() {
			for len(stack) > 0 {
				cur := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				obj := repo.Objects[cur]
				if obj == nil {
					continue
				}
				haveObjects[cur] = true
				if obj.Type == ObjectTree && !seenTrees[cur] {
					seenTrees[cur] = true
					for _, e := range GetTree(repo, cur) {
						stack = append(stack, e.SHA)
					}
				}
			}
		}
		drain()
		// Every commit reachable from plain haves, plus their trees. A
		// shallow-line have contributes only its own tree (queued
		// above), not its ancestors' objects.
		haveCommits := make(map[string]bool)
		var hcQueue []string
		for h := range haves {
			peeled := PeelToNonTag(repo, h)
			if shallowPeeled[peeled] {
				continue
			}
			if GetCommit(repo, peeled) != nil && !haveCommits[peeled] {
				haveCommits[peeled] = true
				hcQueue = append(hcQueue, peeled)
			}
		}
		for i := 0; i < len(hcQueue); i++ {
			commit := GetCommit(repo, hcQueue[i])
			if commit == nil {
				continue
			}
			for _, p := range commit.Parents {
				if !haveCommits[p] {
					haveCommits[p] = true
					hcQueue = append(hcQueue, p)
				}
			}
		}
		for sha := range haveCommits {
			if commit := GetCommit(repo, sha); commit != nil && !seenTrees[commit.Tree] {
				stack = append(stack, commit.Tree)
			}
		}
		drain()
	}

	var emit []PackObject
	emitted := make(map[string]bool)
	pushObj := func(sha string) {
		if emitted[sha] {
			return
		}
		obj := repo.Objects[sha]
		if obj == nil {
			return
		}
		emitted[sha] = true
		emit = append(emit, PackObject{Type: obj.Type, Data: obj.Data, SHA: sha})
	}
	// Wants that are tags (or other non-commit objects) come first with
	// their targets.
	for _, want := range wants {
		if haveObjects[want] || haves[want] {
			continue
		}
		if obj := repo.Objects[want]; obj != nil && obj.Type == ObjectTag {
			pushObj(want)
		}
	}
	// Commits in distance order (nearest want first), then their
	// trees/blobs.
	commitsByDist := make([]string, 0, len(included))
	for sha := range included {
		commitsByDist = append(commitsByDist, sha)
	}
	sort.Slice(commitsByDist, func(i, j int) bool {
		if dist[commitsByDist[i]] != dist[commitsByDist[j]] {
			return dist[commitsByDist[i]] < dist[commitsByDist[j]]
		}
		return commitsByDist[i] < commitsByDist[j]
	})
	var treeQueue []string
	for _, sha := range commitsByDist {
		pushObj(sha)
		if commit := GetCommit(repo, sha); commit != nil {
			treeQueue = append(treeQueue, commit.Tree)
		}
	}
	seenTrees := make(map[string]bool)
	var blobQueue []string
	for len(treeQueue) > 0 {
		cur := treeQueue[len(treeQueue)-1]
		treeQueue = treeQueue[:len(treeQueue)-1]
		if seenTrees[cur] || haveObjects[cur] {
			continue
		}
		seenTrees[cur] = true
		pushObj(cur)
		for _, e := range GetTree(repo, cur) {
			if e.Kind == "tree" {
				treeQueue = append(treeQueue, e.SHA)
			} else {
				blobQueue = append(blobQueue, e.SHA)
			}
		}
	}
	sort.Strings(blobQueue)
	for _, sha := range blobQueue {
		if !haveObjects[sha] {
			pushObj(sha)
		}
	}
	// Tag objects pointing at anything emitted (annotated tags on sent
	// commits). Object shas are scanned in sorted order so emission is
	// deterministic.
	allShas := make([]string, 0, len(repo.Objects))
	for sha := range repo.Objects {
		allShas = append(allShas, sha)
	}
	sort.Strings(allShas)
	grew := true
	for grew {
		grew = false
		for _, sha := range allShas {
			obj := repo.Objects[sha]
			if obj == nil || obj.Type != ObjectTag || emitted[sha] || haveObjects[sha] {
				continue
			}
			if tag := GetTag(repo, sha); tag != nil && emitted[PeelToNonTag(repo, tag.Object)] {
				pushObj(sha)
				grew = true
			}
		}
	}
	return WalkResult{Objects: emit, Shallow: shallow, Unshallow: unshallow}
}

// ---------------------------------------------------------------------------
// pkt-line framing
// ---------------------------------------------------------------------------

// FLUSH is the pkt-line flush packet.
const FLUSH = "0000"

// PktLine frames text as one pkt-line (4-hex-digit length + payload).
func PktLine(text string) []byte {
	payload := []byte(text)
	out := make([]byte, 4, len(payload)+4)
	copy(out, fmt.Sprintf("%04x", len(payload)+4))
	return append(out, payload...)
}

// PktFlush returns the flush packet.
func PktFlush() []byte { return []byte(FLUSH) }

// ParsePktLines parses pkt-lines from the start of buf. It returns the
// lines (without length headers), the bytes consumed, and whether a flush
// packet ended the section. The 0001 delimiter packet is skipped and
// parsing continues; a malformed or truncated length stops parsing
// without error.
func ParsePktLines(buf []byte) (lines []string, consumed int, sawFlush bool) {
	pos := 0
	for pos+4 <= len(buf) {
		lenHex := string(buf[pos : pos+4])
		if lenHex == FLUSH {
			pos += 4
			sawFlush = true
			break
		}
		if lenHex == "0001" {
			pos += 4
			continue
		}
		length := 0
		valid := true
		for _, c := range lenHex {
			length *= 16
			switch {
			case c >= '0' && c <= '9':
				length += int(c - '0')
			case c >= 'a' && c <= 'f':
				length += int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				length += int(c-'A') + 10
			default:
				valid = false
			}
		}
		if !valid || length < 4 || pos+length > len(buf) {
			break
		}
		lines = append(lines, string(buf[pos+4:pos+length]))
		pos += length
	}
	return lines, pos, sawFlush
}

// SidebandEncode encodes a payload into sideband-64k packets (band byte +
// chunks of at most 65515 bytes), plus a flush packet.
func SidebandEncode(payload []byte, band byte) []byte {
	var out bytes.Buffer
	const chunkSize = 65515
	for off := 0; off < len(payload); off += chunkSize {
		end := off + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[off:end]
		fmt.Fprintf(&out, "%04x", len(chunk)+5)
		out.WriteByte(band)
		out.Write(chunk)
	}
	out.Write(PktFlush())
	return out.Bytes()
}

// IsZeroSHA reports whether sha is the all-zero object id.
func IsZeroSHA(sha string) bool { return sha == ZERO_SHA }
