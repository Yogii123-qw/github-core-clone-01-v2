// REST v3 API, domain layer: GitHub-shaped serializers, diff/merge
// computation, and the shared domain operations (create issue / pull /
// merge) used by the REST, GraphQL, and MCP surfaces alike, so all three
// read and write the same store. HTTP routing lives in rest_handlers.go.
//
// Sequence-number discipline: timestamps derive from Store.Seq, so every
// operation consumes sequence numbers in exactly the order the reference
// behavior specifies. Do not add or reorder NextSeq calls casually.
package main

import (
	"bytes"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Serializers
// ---------------------------------------------------------------------------

func userJSON(login string, id int) obj {
	return obj{
		"login":               login,
		"id":                  id,
		"node_id":             GlobalID("User", strconv.Itoa(id)),
		"avatar_url":          "https://avatars.githubusercontent.com/u/" + strconv.Itoa(id) + "?v=4",
		"gravatar_id":         "",
		"url":                 "/api/v3/users/" + login,
		"html_url":            "https://github.com/" + login,
		"followers_url":       "/api/v3/users/" + login + "/followers",
		"following_url":       "/api/v3/users/" + login + "/following{/other_user}",
		"gists_url":           "/api/v3/users/" + login + "/gists{/gist_id}",
		"starred_url":         "/api/v3/users/" + login + "/starred{/owner}{/repo}",
		"subscriptions_url":   "/api/v3/users/" + login + "/subscriptions",
		"organizations_url":   "/api/v3/users/" + login + "/orgs",
		"repos_url":           "/api/v3/users/" + login + "/repos",
		"events_url":          "/api/v3/users/" + login + "/events{/privacy}",
		"received_events_url": "/api/v3/users/" + login + "/received_events",
		"type":                "User",
		"site_admin":          false,
	}
}

// RepoJSON renders a repository in GitHub's shape.
func RepoJSON(store *Store, repo *Repo) obj {
	full := LOGIN + "/" + repo.Name
	openIssues := 0
	for _, i := range repo.Issues {
		if i.State == StateOpen {
			openIssues++
		}
	}
	for _, p := range repo.Pulls {
		if p.State == StateOpen {
			openIssues++
		}
	}
	return obj{
		"id":                          repo.DBID,
		"node_id":                     GlobalID("Repository", strconv.Itoa(repo.DBID)),
		"name":                        repo.Name,
		"full_name":                   full,
		"owner":                       userJSON(LOGIN, 1),
		"private":                     repo.IsPrivate,
		"html_url":                    "https://github.com/" + full,
		"description":                 repo.Description,
		"fork":                        false,
		"url":                         "/api/v3/repos/" + full,
		"archive_url":                 "/api/v3/repos/" + full + "/{archive_format}{/ref}",
		"assignees_url":               "/api/v3/repos/" + full + "/assignees{/user}",
		"blobs_url":                   "/api/v3/repos/" + full + "/git/blobs{/sha}",
		"branches_url":                "/api/v3/repos/" + full + "/branches{/branch}",
		"clone_url":                   "https://github.com/" + full + ".git",
		"collaborators_url":           "/api/v3/repos/" + full + "/collaborators{/collaborator}",
		"comments_url":                "/api/v3/repos/" + full + "/comments{/number}",
		"commits_url":                 "/api/v3/repos/" + full + "/commits{/sha}",
		"compare_url":                 "/api/v3/repos/" + full + "/compare/{base}...{head}",
		"contents_url":                "/api/v3/repos/" + full + "/contents/{+path}",
		"contributors_url":            "/api/v3/repos/" + full + "/contributors",
		"deployments_url":             "/api/v3/repos/" + full + "/deployments",
		"downloads_url":               "/api/v3/repos/" + full + "/downloads",
		"events_url":                  "/api/v3/repos/" + full + "/events",
		"forks_url":                   "/api/v3/repos/" + full + "/forks",
		"git_commits_url":             "/api/v3/repos/" + full + "/git/commits{/sha}",
		"git_refs_url":                "/api/v3/repos/" + full + "/git/refs{/sha}",
		"git_tags_url":                "/api/v3/repos/" + full + "/git/tags{/sha}",
		"git_url":                     "git://github.com/" + full + ".git",
		"issue_comment_url":           "/api/v3/repos/" + full + "/issues/comments{/number}",
		"issue_events_url":            "/api/v3/repos/" + full + "/issues/events{/number}",
		"issues_url":                  "/api/v3/repos/" + full + "/issues{/number}",
		"keys_url":                    "/api/v3/repos/" + full + "/keys{/key_id}",
		"labels_url":                  "/api/v3/repos/" + full + "/labels{/name}",
		"languages_url":               "/api/v3/repos/" + full + "/languages",
		"merges_url":                  "/api/v3/repos/" + full + "/merges",
		"milestones_url":              "/api/v3/repos/" + full + "/milestones{/number}",
		"notifications_url":           "/api/v3/repos/" + full + "/notifications{?since,all,participating}",
		"pulls_url":                   "/api/v3/repos/" + full + "/pulls{/number}",
		"releases_url":                "/api/v3/repos/" + full + "/releases{/id}",
		"ssh_url":                     "git@github.com:" + full + ".git",
		"stargazers_url":              "/api/v3/repos/" + full + "/stargazers",
		"statuses_url":                "/api/v3/repos/" + full + "/statuses/{sha}",
		"subscribers_url":             "/api/v3/repos/" + full + "/subscribers",
		"subscription_url":            "/api/v3/repos/" + full + "/subscription",
		"tags_url":                    "/api/v3/repos/" + full + "/tags",
		"teams_url":                   "/api/v3/repos/" + full + "/teams",
		"trees_url":                   "/api/v3/repos/" + full + "/git/trees{/sha}",
		"created_at":                  store.ISO(repo.CreatedSeq),
		"updated_at":                  store.ISO(repo.UpdatedSeq),
		"pushed_at":                   store.ISO(repo.UpdatedSeq),
		"homepage":                    nil,
		"size":                        len(repo.Objects),
		"stargazers_count":            0,
		"watchers_count":              0,
		"language":                    nil,
		"has_issues":                  true,
		"has_projects":                true,
		"has_downloads":               true,
		"has_wiki":                    true,
		"has_pages":                   false,
		"has_discussions":             false,
		"forks_count":                 0,
		"mirror_url":                  nil,
		"archived":                    false,
		"disabled":                    false,
		"open_issues_count":           openIssues,
		"license":                     nil,
		"allow_forking":               true,
		"is_template":                 false,
		"web_commit_signoff_required": false,
		"topics":                      []any{},
		"visibility":                  visibilityOf(repo.IsPrivate),
		"forks":                       0,
		"open_issues":                 openIssues,
		"watchers":                    0,
		"default_branch":              repo.DefaultBranch,
		"permissions":                 obj{"admin": true, "maintain": true, "push": true, "triage": true, "pull": true},
	}
}

func visibilityOf(isPrivate bool) string {
	if isPrivate {
		return "private"
	}
	return "public"
}

// encodeURIComponent escapes a string the way JavaScript's
// encodeURIComponent does: every byte except [A-Za-z0-9-_.!~*'()] is
// percent-encoded with uppercase hex.
func encodeURIComponent(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) != -1 {
			sb.WriteByte(c)
		} else {
			const hexDigits = "0123456789ABCDEF"
			sb.WriteByte('%')
			sb.WriteByte(hexDigits[c>>4])
			sb.WriteByte(hexDigits[c&0x0f])
		}
	}
	return sb.String()
}

// LabelJSON renders a label in GitHub's shape.
func LabelJSON(store *Store, repo *Repo, label *Label) obj {
	return obj{
		"id":          label.ID,
		"node_id":     GlobalID("Label", strconv.Itoa(repo.DBID), strconv.Itoa(label.ID)),
		"url":         "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/labels/" + encodeURIComponent(label.Name),
		"name":        label.Name,
		"color":       label.Color,
		"default":     false,
		"description": label.Description,
	}
}

func labelsJSON(store *Store, repo *Repo, names []string) []obj {
	out := []obj{}
	for _, name := range names {
		if label, ok := repo.Labels[name]; ok {
			out = append(out, LabelJSON(store, repo, label))
		}
	}
	return out
}

// IssueJSON renders an issue in GitHub's shape. When asPull is non-nil,
// the pull_request block is included, as GitHub does for pull requests
// listed as issues.
func IssueJSON(store *Store, repo *Repo, issue *Issue, asPull *Pull) obj {
	full := LOGIN + "/" + repo.Name
	var body any
	if issue.Body != nil {
		body = *issue.Body
	}
	var closedAt any
	if issue.ClosedSeq != nil {
		closedAt = store.ISO(*issue.ClosedSeq)
	}
	var stateReason any
	if issue.State == StateClosed {
		stateReason = "completed"
	}
	out := obj{
		"id":                       issue.ID,
		"node_id":                  GlobalID("Issue", strconv.Itoa(repo.DBID), strconv.Itoa(issue.Number)),
		"url":                      "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(issue.Number),
		"repository_url":           "/api/v3/repos/" + full,
		"labels_url":               "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(issue.Number) + "/labels{/name}",
		"comments_url":             "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(issue.Number) + "/comments",
		"events_url":               "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(issue.Number) + "/events",
		"html_url":                 "https://github.com/" + full + "/issues/" + strconv.Itoa(issue.Number),
		"number":                   issue.Number,
		"state":                    string(issue.State),
		"title":                    issue.Title,
		"body":                     body,
		"user":                     userJSON(issue.User, 1),
		"labels":                   labelsJSON(store, repo, issue.Labels),
		"assignee":                 nil,
		"locked":                   false,
		"assignees":                []any{},
		"milestone":                nil,
		"comments":                 len(issue.Comments),
		"created_at":               store.ISO(issue.CreatedSeq),
		"updated_at":               store.ISO(issue.UpdatedSeq),
		"closed_at":                closedAt,
		"author_association":       "OWNER",
		"active_lock_reason":       nil,
		"body_text":                body,
		"timeline_url":             "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(issue.Number) + "/timeline",
		"performed_via_github_app": nil,
		"state_reason":             stateReason,
	}
	if asPull != nil {
		var mergedAt any
		if asPull.MergedSeq != nil {
			mergedAt = store.ISO(*asPull.MergedSeq)
		}
		out["pull_request"] = obj{
			"url":       "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(asPull.Number),
			"html_url":  "https://github.com/" + full + "/pull/" + strconv.Itoa(asPull.Number),
			"diff_url":  "https://github.com/" + full + "/pull/" + strconv.Itoa(asPull.Number) + ".diff",
			"patch_url": "https://github.com/" + full + "/pull/" + strconv.Itoa(asPull.Number) + ".patch",
			"merged_at": mergedAt,
		}
	}
	return out
}

// CommentJSON renders a comment in GitHub's shape.
func CommentJSON(store *Store, repo *Repo, issueNumber int, comment *Comment) obj {
	full := LOGIN + "/" + repo.Name
	return obj{
		"id":                       comment.ID,
		"node_id":                  GlobalID("IssueComment", strconv.Itoa(repo.DBID), strconv.Itoa(comment.ID)),
		"url":                      "/api/v3/repos/" + full + "/issues/comments/" + strconv.Itoa(comment.ID),
		"html_url":                 "https://github.com/" + full + "/issues/" + strconv.Itoa(issueNumber) + "#issuecomment-" + strconv.Itoa(comment.ID),
		"body":                     comment.Body,
		"user":                     userJSON(comment.User, 1),
		"created_at":               store.ISO(comment.CreatedSeq),
		"updated_at":               store.ISO(comment.UpdatedSeq),
		"issue_url":                "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(issueNumber),
		"author_association":       "OWNER",
		"performed_via_github_app": nil,
	}
}

// ---------------------------------------------------------------------------
// Diffs and merges
// ---------------------------------------------------------------------------

// FileDiff is one file's diff between two trees.
type FileDiff struct {
	Filename  string
	Status    string // "added", "removed", or "modified"
	Additions int
	Deletions int
	Changes   int
	SHA       string
	Patch     *string
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffOp struct {
	kind string // "same", "add", "del"
	text string
}

// lineDiff computes a line diff via longest-common-subsequence. Files
// whose LCS table would exceed one million cells degrade to a
// whole-file replacement, as in the reference behavior.
func lineDiff(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)
	if n*m > 1000000 {
		ops := make([]diffOp, 0, n+m)
		for _, l := range oldLines {
			ops = append(ops, diffOp{kind: "del", text: l})
		}
		for _, l := range newLines {
			ops = append(ops, diffOp{kind: "add", text: l})
		}
		return ops
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		if oldLines[i] == newLines[j] {
			ops = append(ops, diffOp{kind: "same", text: oldLines[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, diffOp{kind: "del", text: oldLines[i]})
			i++
		} else {
			ops = append(ops, diffOp{kind: "add", text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: "del", text: oldLines[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: "add", text: newLines[j]})
	}
	return ops
}

// buildPatch renders a unified diff with a single hunk covering all ops
// with full context.
func buildPatch(oldPath, newPath string, ops []diffOp) string {
	var sb strings.Builder
	sb.WriteString("--- a/" + oldPath + "\n")
	sb.WriteString("+++ b/" + newPath + "\n")
	oldCount, newCount := 0, 0
	for _, op := range ops {
		if op.kind != "add" {
			oldCount++
		}
		if op.kind != "del" {
			newCount++
		}
	}
	sb.WriteString("@@ -1," + strconv.Itoa(oldCount) + " +1," + strconv.Itoa(newCount) + " @@\n")
	for _, op := range ops {
		switch op.kind {
		case "same":
			sb.WriteString(" ")
		case "add":
			sb.WriteString("+")
		case "del":
			sb.WriteString("-")
		}
		sb.WriteString(op.text)
		sb.WriteString("\n")
	}
	return sb.String()
}

func blobText(repo *Repo, sha string) string {
	obj := repo.Objects[sha]
	if obj == nil || obj.Type != ObjectBlob {
		return ""
	}
	return string(obj.Data)
}

// DiffTrees diffs two trees (either may be "" for the empty tree) and
// returns per-file diffs sorted by path.
func DiffTrees(repo *Repo, baseTree, headTree string) []FileDiff {
	var baseMap, headMap map[string]FlatEntry
	if baseTree == "" {
		baseMap = make(map[string]FlatEntry)
	} else {
		baseMap = FlattenTree(repo, baseTree)
	}
	if headTree == "" {
		headMap = make(map[string]FlatEntry)
	} else {
		headMap = FlattenTree(repo, headTree)
	}
	pathSet := make(map[string]bool)
	for p := range baseMap {
		pathSet[p] = true
	}
	for p := range headMap {
		pathSet[p] = true
	}
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []FileDiff
	for _, path := range paths {
		b, hasB := baseMap[path]
		h, hasH := headMap[path]
		if hasB && hasH && b.SHA == h.SHA {
			continue
		}
		oldText, newText := "", ""
		if hasB {
			oldText = blobText(repo, b.SHA)
		}
		if hasH {
			newText = blobText(repo, h.SHA)
		}
		ops := lineDiff(splitLines(oldText), splitLines(newText))
		additions, deletions := 0, 0
		for _, op := range ops {
			if op.kind == "add" {
				additions++
			}
			if op.kind == "del" {
				deletions++
			}
		}
		status := "modified"
		sha := h.SHA
		if !hasB {
			status = "added"
		} else if !hasH {
			status = "removed"
			sha = b.SHA
		}
		diff := FileDiff{
			Filename:  path,
			Status:    status,
			Additions: additions,
			Deletions: deletions,
			Changes:   additions + deletions,
			SHA:       sha,
		}
		if len(ops) <= 4000 {
			patch := buildPatch(path, path, ops)
			diff.Patch = &patch
		}
		out = append(out, diff)
	}
	return out
}

// MergeInfo is the cached merge analysis for a pull request.
type MergeInfo struct {
	Mergeable    bool
	Conflict     bool
	Files        []FileDiff
	Commits      int
	Additions    int
	Deletions    int
	ChangedFiles int
	MergeBase    string
}

// PRMergeInfo computes (and caches) the merge analysis for a pull
// request: file diffs, commit count, and conflict detection via
// three-way comparison against the merge base.
func PRMergeInfo(store *Store, repo *Repo, pull *Pull) *MergeInfo {
	key := pull.BaseSHA + ":" + pull.HeadSHA
	if v, ok := repo.diffCache.Load(key); ok {
		return v.(*MergeInfo)
	}
	baseCommit := GetCommit(repo, pull.BaseSHA)
	headCommit := GetCommit(repo, pull.HeadSHA)
	baseTree, headTree := "", ""
	if baseCommit != nil {
		baseTree = baseCommit.Tree
	}
	if headCommit != nil {
		headTree = headCommit.Tree
	}
	files := DiffTrees(repo, baseTree, headTree)
	additions, deletions := 0, 0
	for _, f := range files {
		additions += f.Additions
		deletions += f.Deletions
	}
	// Commit count: commits reachable from head but not from base.
	commits := 0
	{
		baseAncestors := make(map[string]bool)
		stack := []string{pull.BaseSHA}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if baseAncestors[cur] {
				continue
			}
			baseAncestors[cur] = true
			if c := GetCommit(repo, cur); c != nil {
				stack = append(stack, c.Parents...)
			}
		}
		seen := make(map[string]bool)
		stack2 := []string{pull.HeadSHA}
		for len(stack2) > 0 {
			cur := stack2[len(stack2)-1]
			stack2 = stack2[:len(stack2)-1]
			if seen[cur] || baseAncestors[cur] {
				continue
			}
			seen[cur] = true
			commits++
			if c := GetCommit(repo, cur); c != nil {
				stack2 = append(stack2, c.Parents...)
			}
		}
	}
	// Conflict: a path changed on both sides since the merge base,
	// differently.
	base := MergeBase(repo, pull.BaseSHA, pull.HeadSHA)
	conflict := false
	if base != "" {
		var ancestorTree string
		if baseC := GetCommit(repo, base); baseC != nil {
			ancestorTree = baseC.Tree
		}
		var aMap map[string]FlatEntry
		if ancestorTree == "" {
			aMap = make(map[string]FlatEntry)
		} else {
			aMap = FlattenTree(repo, ancestorTree)
		}
		var oMap, tMap map[string]FlatEntry
		if baseTree == "" {
			oMap = make(map[string]FlatEntry)
		} else {
			oMap = FlattenTree(repo, baseTree)
		}
		if headTree == "" {
			tMap = make(map[string]FlatEntry)
		} else {
			tMap = FlattenTree(repo, headTree)
		}
		pathSet := make(map[string]bool)
		for p := range aMap {
			pathSet[p] = true
		}
		for p := range oMap {
			pathSet[p] = true
		}
		for p := range tMap {
			pathSet[p] = true
		}
		for path := range pathSet {
			a, hasA := aMap[path]
			o, hasO := oMap[path]
			t, hasT := tMap[path]
			// Absent entries compare as "" — stored shas are never
			// empty, so "" is an unambiguous absent sentinel, matching
			// the reference null comparison.
			aSHA, oSHA, tSHA := "", "", ""
			if hasA {
				aSHA = a.SHA
			}
			if hasO {
				oSHA = o.SHA
			}
			if hasT {
				tSHA = t.SHA
			}
			if oSHA != aSHA && tSHA != aSHA && oSHA != tSHA {
				conflict = true
				break
			}
		}
	} else {
		// No common ancestor: any path present with different content
		// on both sides is a conflict.
		var oMap, tMap map[string]FlatEntry
		if baseTree == "" {
			oMap = make(map[string]FlatEntry)
		} else {
			oMap = FlattenTree(repo, baseTree)
		}
		if headTree == "" {
			tMap = make(map[string]FlatEntry)
		} else {
			tMap = FlattenTree(repo, headTree)
		}
		for path, o := range oMap {
			if t, ok := tMap[path]; ok && t.SHA != o.SHA {
				conflict = true
				break
			}
		}
	}
	info := &MergeInfo{
		Mergeable:    !conflict,
		Conflict:     conflict,
		Files:        files,
		Commits:      commits,
		Additions:    additions,
		Deletions:    deletions,
		ChangedFiles: len(files),
		MergeBase:    base,
	}
	if repo.diffCount.Load() > 512 {
		repo.diffCache.Range(func(k, _ any) bool {
			repo.diffCache.Delete(k)
			return true
		})
		repo.diffCount.Store(0)
	}
	repo.diffCache.Store(key, info)
	repo.diffCount.Add(1)
	_ = store
	return info
}

// PRJSON renders a pull request in GitHub's shape.
func PRJSON(store *Store, repo *Repo, pull *Pull) obj {
	full := LOGIN + "/" + repo.Name
	info := PRMergeInfo(store, repo, pull)
	headRepo := RepoJSON(store, repo)
	var body any
	if pull.Body != nil {
		body = *pull.Body
	}
	var closedAt, mergedAt any
	if pull.ClosedSeq != nil {
		closedAt = store.ISO(*pull.ClosedSeq)
	}
	if pull.MergedSeq != nil {
		mergedAt = store.ISO(*pull.MergedSeq)
	}
	var mergeSHA any
	if pull.MergeSHA != nil {
		mergeSHA = *pull.MergeSHA
	}
	var mergedBy any
	if pull.Merged {
		mergedBy = userJSON(pull.User, 1)
	}
	mergeableState := "unknown"
	if pull.State == StateOpen {
		if info.Conflict {
			mergeableState = "dirty"
		} else {
			mergeableState = "clean"
		}
	}
	open := pull.State == StateOpen
	return obj{
		"id":                  pull.ID,
		"node_id":             GlobalID("PullRequest", strconv.Itoa(repo.DBID), strconv.Itoa(pull.Number)),
		"url":                 "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(pull.Number),
		"html_url":            "https://github.com/" + full + "/pull/" + strconv.Itoa(pull.Number),
		"diff_url":            "https://github.com/" + full + "/pull/" + strconv.Itoa(pull.Number) + ".diff",
		"patch_url":           "https://github.com/" + full + "/pull/" + strconv.Itoa(pull.Number) + ".patch",
		"issue_url":           "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(pull.Number),
		"number":              pull.Number,
		"state":               string(pull.State),
		"locked":              false,
		"title":               pull.Title,
		"user":                userJSON(pull.User, 1),
		"body":                body,
		"created_at":          store.ISO(pull.CreatedSeq),
		"updated_at":          store.ISO(pull.UpdatedSeq),
		"closed_at":           closedAt,
		"merged_at":           mergedAt,
		"merge_commit_sha":    mergeSHA,
		"assignee":            nil,
		"assignees":           []any{},
		"requested_reviewers": []any{},
		"requested_teams":     []any{},
		"labels":              []any{},
		"milestone":           nil,
		"draft":               false,
		"commits_url":         "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(pull.Number) + "/commits",
		"review_comments_url": "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(pull.Number) + "/comments",
		"review_comment_url":  "/api/v3/repos/" + full + "/pulls/comments{/number}",
		"comments_url":        "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(pull.Number) + "/comments",
		"statuses_url":        "/api/v3/repos/" + full + "/statuses/" + pull.HeadSHA,
		"head": obj{
			"label": LOGIN + ":" + pull.HeadRef,
			"ref":   pull.HeadRef,
			"sha":   pull.HeadSHA,
			"user":  userJSON(LOGIN, 1),
			"repo":  headRepo,
		},
		"base": obj{
			"label": LOGIN + ":" + pull.BaseRef,
			"ref":   pull.BaseRef,
			"sha":   pull.BaseSHA,
			"user":  userJSON(LOGIN, 1),
			"repo":  headRepo,
		},
		"_links": obj{
			"self":            obj{"href": "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(pull.Number)},
			"html":            obj{"href": "https://github.com/" + full + "/pull/" + strconv.Itoa(pull.Number)},
			"issue":           obj{"href": "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(pull.Number)},
			"comments":        obj{"href": "/api/v3/repos/" + full + "/issues/" + strconv.Itoa(pull.Number) + "/comments"},
			"review_comments": obj{"href": "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(pull.Number) + "/comments"},
			"review_comment":  obj{"href": "/api/v3/repos/" + full + "/pulls/comments{/number}"},
			"commits":         obj{"href": "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(pull.Number) + "/commits"},
			"statuses":        obj{"href": "/api/v3/repos/" + full + "/statuses/" + pull.HeadSHA},
		},
		"author_association":    "OWNER",
		"auto_merge":            nil,
		"active_lock_reason":    nil,
		"merged":                pull.Merged,
		"mergeable":             open && info.Mergeable,
		"rebaseable":            open && info.Mergeable,
		"mergeable_state":       mergeableState,
		"merged_by":             mergedBy,
		"comments":              len(pull.Comments),
		"review_comments":       0,
		"maintainer_can_modify": false,
		"commits":               info.Commits,
		"additions":             info.Additions,
		"deletions":             info.Deletions,
		"changed_files":         info.ChangedFiles,
	}
}

// ---------------------------------------------------------------------------
// Shared domain operations
// ---------------------------------------------------------------------------

// ParseLabelNames parses a labels value: absent means no labels; a JSON
// array of strings or {name} objects yields names; anything else is
// invalid. present reports whether the value was supplied at all
// (absent and JSON null differ: absent is valid, null is not).
func ParseLabelNames(raw any, present bool) ([]string, bool) {
	if !present {
		return []string{}, true
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	names := []string{}
	for _, item := range arr {
		switch v := item.(type) {
		case string:
			names = append(names, v)
		case map[string]any:
			if name, ok := v["name"].(string); ok {
				names = append(names, name)
			} else {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return names, true
}

// CreateIssueRecord validates labels and creates an issue. On failure it
// returns the REST error result to answer with.
func CreateIssueRecord(store *Store, repo *Repo, title string, body *string, labelNames []string) (*Issue, *RestResult) {
	for _, name := range labelNames {
		if _, ok := repo.Labels[name]; !ok {
			err := validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "custom"}}, "")
			return nil, &err
		}
	}
	seq := store.NextSeq()
	issue := &Issue{
		ID:         store.NextID(),
		Number:     repo.NextNumber,
		Title:      title,
		Body:       body,
		State:      StateOpen,
		Labels:     append([]string(nil), labelNames...),
		User:       LOGIN,
		CreatedSeq: seq,
		UpdatedSeq: seq,
		Comments:   nil,
	}
	repo.NextNumber++
	repo.Issues = append(repo.Issues, issue)
	repo.IssueByNumber[issue.Number] = issue
	repo.UpdatedSeq = seq
	repo.Touch()
	return issue, nil
}

// SplitHeadRef splits "owner:ref" into owner and ref; a bare ref belongs
// to the viewer.
func SplitHeadRef(head string) (owner, ref string) {
	if idx := strings.Index(head, ":"); idx != -1 {
		return head[:idx], head[idx+1:]
	}
	return LOGIN, head
}

// isHex40 reports whether s is exactly 40 lowercase hex digits.
func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isLowerHex(s[i]) {
			return false
		}
	}
	return true
}

// isHexPrefix reports whether s is 7 to 39 lowercase hex digits.
func isHexPrefix(s string) bool {
	if len(s) < 7 || len(s) > 39 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isLowerHex(s[i]) {
			return false
		}
	}
	return true
}

func isLowerHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}

// ResolveRef resolves a refish to an object sha: HEAD, a full ref name,
// a branch or tag short name, a full sha, or a unique sha prefix.
// It returns "" when unresolvable.
func ResolveRef(repo *Repo, refish string) string {
	if refish == "HEAD" {
		if sha, ok := repo.Refs["refs/heads/"+repo.DefaultBranch]; ok {
			return sha
		}
		return ""
	}
	if sha, ok := repo.Refs[refish]; ok {
		return sha
	}
	if sha, ok := repo.Refs["refs/heads/"+refish]; ok {
		return sha
	}
	if sha, ok := repo.Refs["refs/tags/"+refish]; ok {
		return sha
	}
	if isHex40(refish) {
		if _, ok := repo.Objects[refish]; ok {
			return refish
		}
		return ""
	}
	if isHexPrefix(refish) {
		found := ""
		for key := range repo.Objects {
			if strings.HasPrefix(key, refish) {
				if found != "" {
					return ""
				}
				found = key
			}
		}
		return found
	}
	return ""
}

// CreatePullRecord validates head/base and creates a pull request. On
// failure it returns the REST error result to answer with.
func CreatePullRecord(store *Store, repo *Repo, title string, body *string, head, base string) (*Pull, *RestResult) {
	_, headRef := SplitHeadRef(head)
	_, baseRef := SplitHeadRef(base)
	headSHA := ResolveRef(repo, headRef)
	baseSHA := ResolveRef(repo, baseRef)
	if headSHA == "" || baseSHA == "" {
		field := "base"
		if headSHA == "" {
			field = "head"
		}
		err := validationFailed([]validationError{{Resource: "PullRequest", Field: field, Code: "custom"}}, "")
		return nil, &err
	}
	headCommit := ResolveToCommit(repo, headSHA)
	baseCommit := ResolveToCommit(repo, baseSHA)
	if headCommit == "" || baseCommit == "" {
		err := validationFailed([]validationError{{Resource: "PullRequest", Field: "head", Code: "custom"}}, "")
		return nil, &err
	}
	if headCommit == baseCommit {
		err := validationFailed([]validationError{{Resource: "PullRequest", Field: "head", Code: "custom"}}, "No commits between base and head")
		return nil, &err
	}
	seq := store.NextSeq()
	pull := &Pull{
		ID:         store.NextID(),
		Number:     repo.NextNumber,
		Title:      title,
		Body:       body,
		State:      StateOpen,
		HeadRef:    headRef,
		BaseRef:    baseRef,
		HeadSHA:    headCommit,
		BaseSHA:    baseCommit,
		User:       LOGIN,
		CreatedSeq: seq,
		UpdatedSeq: seq,
		Comments:   nil,
	}
	repo.NextNumber++
	repo.Pulls = append(repo.Pulls, pull)
	repo.PullByNumber[pull.Number] = pull
	repo.UpdatedSeq = seq
	repo.Touch()
	return pull, nil
}

func gitIdentity(store *Store, seq int, name, email *string) string {
	n := LOGIN
	if name != nil {
		n = *name
	}
	e := LOGIN + "@users.noreply.github.com"
	if email != nil {
		e = *email
	}
	return n + " <" + e + "> " + strconv.FormatInt(store.UnixSeconds(seq), 10) + " +0000"
}

// Merge error kinds returned by MergePullRecord.
const (
	MergeErrNone     = ""
	MergeErrMerged   = "merged"
	MergeErrConflict = "conflict"
)

// MergePullRecord merges a pull request as one squash commit on its base
// branch. It returns the new commit sha, or an error kind: MergeErrMerged
// (already merged/closed) or MergeErrConflict. commitTitle and
// commitMessage override the squash commit message when non-nil and
// non-empty (commitMessage) following the reference rules.
func MergePullRecord(store *Store, repo *Repo, pull *Pull, commitTitle, commitMessage *string) (string, string) {
	if pull.Merged || pull.State != StateOpen {
		return "", MergeErrMerged
	}
	// Re-resolve the live tips: the PR records the tips at creation, but
	// the branches may have moved since.
	if liveHead := ResolveRef(repo, pull.HeadRef); liveHead != "" {
		if c := ResolveToCommit(repo, liveHead); c != "" {
			pull.HeadSHA = c
		}
	}
	if liveBase := ResolveRef(repo, pull.BaseRef); liveBase != "" {
		if c := ResolveToCommit(repo, liveBase); c != "" {
			pull.BaseSHA = c
		}
	}
	info := PRMergeInfo(store, repo, pull)
	if info.Conflict {
		return "", MergeErrConflict
	}
	seq := store.NextSeq()
	if IsAncestor(repo, pull.HeadSHA, pull.BaseSHA) {
		// Head is already contained in base: nothing to commit.
		mergeSHA := pull.BaseSHA
		pull.Merged = true
		pull.State = StateClosed
		pull.MergeSHA = &mergeSHA
		pull.MergedSeq = &seq
		pull.ClosedSeq = &seq
		pull.UpdatedSeq = seq
		repo.Touch()
		return pull.BaseSHA, MergeErrNone
	}
	// Squash result tree: apply the head side's changes onto the base
	// tree.
	baseCommit := GetCommit(repo, pull.BaseSHA)
	headCommit := GetCommit(repo, pull.HeadSHA)
	var ancestorTree string
	if info.MergeBase != "" {
		if c := GetCommit(repo, info.MergeBase); c != nil {
			ancestorTree = c.Tree
		}
	}
	var aMap map[string]FlatEntry
	if ancestorTree == "" {
		aMap = make(map[string]FlatEntry)
	} else {
		aMap = FlattenTree(repo, ancestorTree)
	}
	oMap := FlattenTree(repo, baseCommit.Tree)
	tMap := FlattenTree(repo, headCommit.Tree)
	result := make(map[string]FlatEntry, len(oMap))
	for k, v := range oMap {
		result[k] = v
	}
	pathSet := make(map[string]bool)
	for p := range aMap {
		pathSet[p] = true
	}
	for p := range tMap {
		pathSet[p] = true
	}
	for path := range pathSet {
		a, hasA := aMap[path]
		t, hasT := tMap[path]
		aSHA, tSHA := "", ""
		if hasA {
			aSHA = a.SHA
		}
		if hasT {
			tSHA = t.SHA
		}
		if tSHA != aSHA {
			if !hasT {
				delete(result, path)
			} else {
				result[path] = FlatEntry{SHA: t.SHA, Mode: t.Mode}
			}
		}
	}
	treeSHA := TreeFromFlat(repo, result)
	message := pull.Title
	if commitTitle != nil {
		message = *commitTitle
	}
	fullMessage := message
	if commitMessage != nil && *commitMessage != "" {
		fullMessage = message + "\n\n" + *commitMessage
	}
	ident := gitIdentity(store, seq, nil, nil)
	sha := BuildCommit(repo, treeSHA, []string{pull.BaseSHA}, ident, ident, fullMessage)
	repo.Refs["refs/heads/"+pull.BaseRef] = sha
	pull.Merged = true
	pull.State = StateClosed
	pull.MergeSHA = &sha
	pull.MergedSeq = &seq
	pull.ClosedSeq = &seq
	pull.UpdatedSeq = seq
	repo.UpdatedSeq = seq
	repo.Touch()
	return sha, MergeErrNone
}

// decodeBase64Lenient decodes base64 the way Node's Buffer.from does:
// whitespace is ignored and decoding is lenient about padding.
func decodeBase64Lenient(s string) ([]byte, error) {
	cleaned := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
			continue
		}
		cleaned = append(cleaned, c)
	}
	// Node ignores characters outside the base64 alphabet; mirror that
	// by filtering before decoding.
	filtered := make([]byte, 0, len(cleaned))
	for _, c := range cleaned {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
			filtered = append(filtered, c)
		}
	}
	if m := len(filtered) % 4; m != 0 {
		filtered = append(filtered, bytes.Repeat([]byte("="), 4-m)...)
	}
	return base64.StdEncoding.DecodeString(string(filtered))
}
