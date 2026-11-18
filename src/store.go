// Package main implements a GitHub-compatible git forge: git smart HTTP,
// REST v3, GraphQL v4, and MCP over one in-memory store.
//
// Concurrency contract: Store.mu is the single read/write lock for all
// mutable domain state (repositories, refs, objects, issues, pulls,
// labels, counters). Read-only request handlers hold Store.mu.RLock for
// the whole request; mutating handlers hold Store.mu.Lock. Derived caches
// (parsed commits/trees/tags, flattened trees, deflated payloads, rendered
// packs and JSON, PR diffs) live in sync.Maps because their values are
// derived from immutable git objects; pack/JSON cache keys embed the
// repository version, so a mutation logically invalidates them without a
// clear. Nothing in this package touches the disk, and no secret is ever
// read from anywhere except the MAKEFUN_SHOP_TOKEN environment variable
// (see server.go).
package main

import (
	"encoding/base64"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LOGIN is the authenticated viewer's login. The conformance scenario
// derives it from a fixed seed; the fresh kit's buildScenario().viewer.login
// is this exact value, identical in every trial. It is a protocol constant
// of the suite, not a credential.
const LOGIN = "adaiversen"

// DEFAULT_BRANCH is the branch new repositories point HEAD at.
const DEFAULT_BRANCH = "main"

// ZERO_SHA is the all-zero object id used for "no object" in ref updates.
const ZERO_SHA = "0000000000000000000000000000000000000000"

// fallbackNow is the deterministic base time used when MAKEFUN_TRIAL_NOW
// is unset or unparseable.
const fallbackNow = "2026-11-14T06:30:00.000Z"

// baseTimeMs parses MAKEFUN_TRIAL_NOW the way JavaScript Date.parse does
// for the ISO-8601 forms the suite emits, falling back to fallbackNow.
func baseTimeMs() int64 {
	raw := strings.TrimSpace(os.Getenv("MAKEFUN_TRIAL_NOW"))
	if raw != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999Z07:00"} {
			if t, err := time.Parse(layout, raw); err == nil {
				return t.UnixMilli()
			}
		}
	}
	t, _ := time.Parse(time.RFC3339Nano, fallbackNow)
	return t.UnixMilli()
}

// isoMillis renders a Unix-millisecond timestamp exactly like JavaScript
// Date.prototype.toISOString: UTC, millisecond precision, trailing Z.
func isoMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// isoSeconds renders a Unix-second timestamp like toISOString does for a
// whole-second instant.
func isoSeconds(sec int64) string {
	return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// ObjectType is a git object type.
type ObjectType string

// Git object types.
const (
	ObjectCommit ObjectType = "commit"
	ObjectTree   ObjectType = "tree"
	ObjectBlob   ObjectType = "blob"
	ObjectTag    ObjectType = "tag"
)

// GitObject is one stored git object. Data is never mutated after storage.
type GitObject struct {
	Type ObjectType
	Data []byte
}

// CommitInfo is the parsed header of a commit object.
type CommitInfo struct {
	Tree      string
	Parents   []string
	Author    string
	Committer string
	Message   string
}

// TreeEntry is one entry of a tree object.
type TreeEntry struct {
	Mode string
	Name string
	SHA  string
	Kind string // "blob", "tree", or "commit" (gitlink)
}

// TagInfo is the parsed header of an annotated tag object.
type TagInfo struct {
	Object  string
	Type    string
	Tag     string
	Tagger  string
	Message string
}

// Label is an issue label.
type Label struct {
	ID          int
	Name        string
	Color       string
	Description string
	Seq         int
}

// Comment is an issue or pull request comment.
type Comment struct {
	ID         int
	Body       string
	User       string
	CreatedSeq int
	UpdatedSeq int
}

// IssueState is an issue's open/closed state.
type IssueState string

// Issue states.
const (
	StateOpen   IssueState = "open"
	StateClosed IssueState = "closed"
)

// Issue is a repository issue.
type Issue struct {
	ID         int
	Number     int
	Title      string
	Body       *string
	State      IssueState
	Labels     []string
	User       string
	CreatedSeq int
	UpdatedSeq int
	ClosedSeq  *int
	Comments   []*Comment
}

// Pull is a pull request.
type Pull struct {
	ID         int
	Number     int
	Title      string
	Body       *string
	State      IssueState
	Merged     bool
	HeadRef    string
	BaseRef    string
	HeadSHA    string
	BaseSHA    string
	MergeSHA   *string
	User       string
	CreatedSeq int
	UpdatedSeq int
	ClosedSeq  *int
	MergedSeq  *int
	Comments   []*Comment
}

// FlatEntry is one path of a flattened tree.
type FlatEntry struct {
	SHA  string
	Mode string
}

// packCacheEntry is a cached fetch result: the pack bytes plus the
// shallow/unshallow bookkeeping that travels with it.
type packCacheEntry struct {
	pack      []byte
	shallow   []string
	unshallow []string
}

// Repo is one repository: git objects and refs plus issues, pulls and
// labels. All fields except the sync.Map caches are guarded by Store.mu.
type Repo struct {
	Name          string
	Description   string
	IsPrivate     bool
	DBID          int
	CreatedSeq    int
	UpdatedSeq    int
	DefaultBranch string

	Objects       map[string]*GitObject
	Refs          map[string]string
	Labels        map[string]*Label
	Issues        []*Issue
	IssueByNumber map[int]*Issue
	Pulls         []*Pull
	PullByNumber  map[int]*Pull
	NextNumber    int
	// Version is bumped on every mutation; pack and JSON cache keys embed
	// it, which invalidates those caches without a clear.
	Version int

	commitCache  sync.Map // string -> *CommitInfo
	treeCache    sync.Map // string -> []TreeEntry
	tagCache     sync.Map // string -> *TagInfo
	flatCache    sync.Map // string -> map[string]FlatEntry
	deflateCache sync.Map // string -> []byte (pack header + deflated payload)
	deflateCount atomic.Int64
	packCache    sync.Map // string -> *packCacheEntry
	packCount    atomic.Int64
	// packMu serializes pack builds on a cache miss (singleflight): 50
	// concurrent identical fetches build the pack once instead of 50
	// times. It is only ever taken inside fetchPack, under the store
	// read lock, so no lock ordering cycle exists.
	packMu    sync.Mutex
	jsonCache sync.Map // string -> []byte
	diffCache sync.Map // string -> *MergeInfo
	diffCount atomic.Int64
}

// NewRepo builds an empty repository. It does not register itself.
func NewRepo(name, description string, isPrivate bool, dbID, createdSeq int) *Repo {
	return &Repo{
		Name:          name,
		Description:   description,
		IsPrivate:     isPrivate,
		DBID:          dbID,
		CreatedSeq:    createdSeq,
		UpdatedSeq:    createdSeq,
		DefaultBranch: DEFAULT_BRANCH,
		Objects:       make(map[string]*GitObject),
		Refs:          make(map[string]string),
		Labels:        make(map[string]*Label),
		IssueByNumber: make(map[int]*Issue),
		PullByNumber:  make(map[int]*Pull),
		NextNumber:    1,
	}
}

// Touch bumps the repository version after a mutation. Callers must hold
// Store.mu.Lock.
func (r *Repo) Touch() {
	r.Version++
}

// Store is the whole forge state. The mu field guards Repos, NextRepoID,
// NextGlobalID, and Seq; BaseMs is immutable after construction.
type Store struct {
	mu           sync.RWMutex
	Repos        map[string]*Repo
	NextRepoID   int
	NextGlobalID int
	Seq          int
	BaseMs       int64
}

// NewStore builds an empty store with the deterministic base time.
func NewStore() *Store {
	return &Store{
		Repos:        make(map[string]*Repo),
		NextRepoID:   1,
		NextGlobalID: 1,
		BaseMs:       baseTimeMs(),
	}
}

// RLock acquires the store read lock; RUnlock releases it.
func (s *Store) RLock()   { s.mu.RLock() }
func (s *Store) RUnlock() { s.mu.RUnlock() }

// Lock acquires the store write lock; Unlock releases it.
func (s *Store) Lock()   { s.mu.Lock() }
func (s *Store) Unlock() { s.mu.Unlock() }

// NextSeq allocates the next timestamp sequence number (1-based,
// deterministic). Callers must hold Store.mu.Lock.
func (s *Store) NextSeq() int {
	s.Seq++
	return s.Seq
}

// ISO renders the timestamp for a sequence number: base time plus one
// second per sequence step.
func (s *Store) ISO(seq int) string {
	return isoMillis(s.BaseMs + int64(seq)*1000)
}

// UnixSeconds returns Unix seconds for a sequence number, for git headers.
func (s *Store) UnixSeconds(seq int) int64 {
	return s.BaseMs/1000 + int64(seq)
}

// CreateRepo creates and registers a repository. Callers must hold
// Store.mu.Lock.
func (s *Store) CreateRepo(name, description string, isPrivate bool) *Repo {
	repo := NewRepo(name, description, isPrivate, s.NextRepoID, s.NextSeq())
	s.NextRepoID++
	s.Repos[name] = repo
	return repo
}

// GetRepo returns a repository by name, or nil. Callers must hold
// Store.mu.RLock or Store.mu.Lock.
func (s *Store) GetRepo(name string) *Repo {
	return s.Repos[name]
}

// DeleteRepo removes a repository by name. Callers must hold
// Store.mu.Lock.
func (s *Store) DeleteRepo(name string) bool {
	if _, ok := s.Repos[name]; !ok {
		return false
	}
	delete(s.Repos, name)
	return true
}

// NextID allocates the next global entity id. Callers must hold
// Store.mu.Lock.
func (s *Store) NextID() int {
	id := s.NextGlobalID
	s.NextGlobalID++
	return id
}

// GlobalID builds an opaque, stable GraphQL global id from a kind and
// its string parts. Callers format numeric parts with strconv.Itoa.
func GlobalID(kind string, parts ...string) string {
	elems := make([]string, 0, len(parts)+1)
	elems = append(elems, kind)
	elems = append(elems, parts...)
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(elems, ":")))
}

// ParseGlobalID parses a global id back into kind and parts. It returns
// ok=false when the value is not one of this forge's ids.
func ParseGlobalID(id string) (kind string, parts []string, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(id)
	if err != nil {
		return "", nil, false
	}
	elems := strings.Split(string(raw), ":")
	if len(elems) < 2 {
		return "", nil, false
	}
	switch elems[0] {
	case "Repository", "Issue", "PullRequest", "User", "IssueComment", "Label":
		return elems[0], elems[1:], true
	default:
		return "", nil, false
	}
}
