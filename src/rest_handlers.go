// REST v3 API, handler layer: pagination, route handlers, and the main
// dispatcher. Serialization and domain operations live in rest.go.
// Callers must hold Store.mu.RLock for read-only routes and Store.mu.Lock
// for mutating routes; server.go enforces that split.
package main

import (
	"encoding/base64"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// RestContext is one REST request's inputs.
type RestContext struct {
	Method   string
	Segments []string
	Query    url.Values
	Body     any
	HasBody  bool
	Store    *Store
	Host     string
}

// RestResult is one REST answer: either JSON to render, or Raw bytes to
// write verbatim (cached renderings).
type RestResult struct {
	Status  int
	JSON    any
	Headers map[string]string
	Raw     []byte
}

func errResult(status int, message string, extra obj) RestResult {
	body := obj{"message": message}
	for k, v := range extra {
		body[k] = v
	}
	return RestResult{Status: status, JSON: body}
}

func notFound() RestResult {
	return errResult(404, "Not Found", obj{"documentation_url": "https://docs.github.com/rest"})
}

// validationError is one entry of a 422 errors array.
type validationError struct {
	Resource string
	Field    string
	Code     string
}

func validationFailed(errors []validationError, message string) RestResult {
	if message == "" {
		message = "Validation Failed"
	}
	list := make([]obj, 0, len(errors))
	for _, e := range errors {
		list = append(list, obj{"resource": e.Resource, "field": e.Field, "code": e.Code})
	}
	return errResult(422, message, obj{"errors": list})
}

// ---------------------------------------------------------------------------
// Pagination and sorting helpers
// ---------------------------------------------------------------------------

// jsParseInt parses a leading integer like JavaScript parseInt(s, 10):
// leading whitespace, optional sign, then digits. It reports ok=false
// when no digits follow. Saturates at 1e15 to stay in int range.
func jsParseInt(s string) (int, bool) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\v' || s[i] == '\f') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	val := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		val = val*10 + int(s[i]-'0')
		if val > 1000000000000000 {
			val = 1000000000000000
		}
		i++
	}
	if i == start {
		return 0, false
	}
	if neg {
		val = -val
	}
	return val, true
}

// firstQuery returns the first value for key and whether key was present.
func firstQuery(q url.Values, key string) (string, bool) {
	vals, ok := q[key]
	if !ok || len(vals) == 0 {
		return "", false
	}
	return vals[0], true
}

// queryOr returns the first value for key, or def when key is absent. A
// present-but-empty value is returned as "".
func queryOr(q url.Values, key, def string) string {
	if v, ok := firstQuery(q, key); ok {
		return v
	}
	return def
}

func pageParams(query url.Values) (page, perPage int) {
	pageStr, pagePresent := firstQuery(query, "page")
	if !pagePresent {
		pageStr = "1"
	}
	if n, ok := jsParseInt(pageStr); ok && n != 0 {
		page = n
	} else {
		page = 1
	}
	if page < 1 {
		page = 1
	}
	perPageStr, perPagePresent := firstQuery(query, "per_page")
	if !perPagePresent {
		perPageStr = "30"
	}
	if n, ok := jsParseInt(perPageStr); ok && n >= 1 {
		perPage = n
	} else {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func paginateLen(total int, query url.Values) (start, end, pageNum, perPage int) {
	pageNum, perPage = pageParams(query)
	start = (pageNum - 1) * perPage
	if start > total {
		start = total
	}
	end = start + perPage
	if end > total {
		end = total
	}
	return start, end, pageNum, perPage
}

func linkHeader(host, path string, query url.Values, pageNum, perPage, total int) map[string]string {
	lastPage := (total + perPage - 1) / perPage
	if lastPage < 1 {
		lastPage = 1
	}
	build := func(page int) string {
		q := url.Values{}
		for k, vs := range query {
			q[k] = append([]string(nil), vs...)
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(perPage))
		return "<http://" + host + path + "?" + q.Encode() + ">"
	}
	var links []string
	if pageNum < lastPage {
		links = append(links, build(pageNum+1)+`; rel="next"`)
	}
	if lastPage > 1 {
		links = append(links, build(lastPage)+`; rel="last"`)
	}
	if pageNum > 1 {
		links = append(links, build(1)+`; rel="first"`)
		links = append(links, build(pageNum-1)+`; rel="prev"`)
	}
	if len(links) == 0 {
		return nil
	}
	return map[string]string{"link": strings.Join(links, ", ")}
}

// ---------------------------------------------------------------------------
// Route handlers
// ---------------------------------------------------------------------------

func bodyObject(ctx *RestContext) map[string]any {
	if m, ok := ctx.Body.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func requireRepo(ctx *RestContext, owner, name string) (*Repo, *RestResult) {
	if owner != LOGIN {
		err := notFound()
		return nil, &err
	}
	repo := ctx.Store.GetRepo(name)
	if repo == nil {
		err := notFound()
		return nil, &err
	}
	return repo, nil
}

func listRepos(ctx *RestContext) RestResult {
	repos := make([]*Repo, 0, len(ctx.Store.Repos))
	for _, r := range ctx.Store.Repos {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].DBID < repos[j].DBID })
	start, end, pageNum, perPage := paginateLen(len(repos), ctx.Query)
	items := make([]obj, 0, end-start)
	for _, r := range repos[start:end] {
		items = append(items, RepoJSON(ctx.Store, r))
	}
	return RestResult{Status: 200, JSON: items, Headers: linkHeader(ctx.Host, "/api/v3/user/repos", ctx.Query, pageNum, perPage, len(repos))}
}

// isValidRepoName reports whether name is non-empty and made only of
// letters, digits, '.', '_' and '-'.
func isValidRepoName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func createRepo(ctx *RestContext) RestResult {
	body := bodyObject(ctx)
	name, _ := body["name"].(string)
	if _, ok := body["name"].(string); !ok || name == "" {
		return validationFailed([]validationError{{Resource: "Repository", Field: "name", Code: "missing"}}, "")
	}
	if !isValidRepoName(name) {
		return validationFailed([]validationError{{Resource: "Repository", Field: "name", Code: "invalid"}}, "")
	}
	if ctx.Store.GetRepo(name) != nil {
		return validationFailed([]validationError{{Resource: "Repository", Field: "name", Code: "already_exists"}}, "Validation Failed: name already exists on this account")
	}
	description, _ := body["description"].(string)
	isPrivate := body["private"] == true
	repo := ctx.Store.CreateRepo(name, description, isPrivate)
	return RestResult{Status: 201, JSON: RepoJSON(ctx.Store, repo)}
}

func listBranches(ctx *RestContext, repo *Repo) RestResult {
	refs := sortedRefs(repo)
	var branches []obj
	for _, ref := range refs {
		if !strings.HasPrefix(ref[0], "refs/heads/") {
			continue
		}
		name := strings.TrimPrefix(ref[0], "refs/heads/")
		branches = append(branches, obj{
			"name":      name,
			"commit":    obj{"sha": ref[1], "url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/commits/" + ref[1]},
			"protected": false,
		})
	}
	start, end, pageNum, perPage := paginateLen(len(branches), ctx.Query)
	page := branches[start:end]
	if page == nil {
		page = []obj{}
	}
	return RestResult{Status: 200, JSON: page, Headers: linkHeader(ctx.Host, "/api/v3/repos/"+LOGIN+"/"+repo.Name+"/branches", ctx.Query, pageNum, perPage, len(branches))}
}

func getBranch(ctx *RestContext, repo *Repo, branch string) RestResult {
	sha, ok := repo.Refs["refs/heads/"+branch]
	if !ok {
		return notFound()
	}
	commit := GetCommit(repo, sha)
	inner := obj{"sha": sha, "url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/commits/" + sha}
	if commit != nil {
		inner["commit"] = obj{"message": commit.Message}
	}
	return RestResult{Status: 200, JSON: obj{
		"name":       branch,
		"commit":     inner,
		"protected":  false,
		"protection": obj{"enabled": false},
	}}
}

// matchIdent parses a git ident line, "Name <email> <seconds> <tz>",
// mirroring the reference pattern's leftmost, non-greedy match: the
// earliest " <" and the earliest ">" after it whose tail matches win.
// It returns the name, email, and decimal seconds, or ok=false.
func matchIdent(line string) (name, email, digits string, ok bool) {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != ' ' || line[i+1] != '<' {
			continue
		}
		for j := i + 2; j < len(line); j++ {
			if line[j] != '>' {
				continue
			}
			d, good := identTail(line[j+1:])
			if !good {
				continue
			}
			return line[:i], line[i+2 : j], d, true
		}
	}
	return "", "", "", false
}

// identTail matches " <digits> <tz>" against the whole remainder.
func identTail(rest string) (digits string, ok bool) {
	if len(rest) < 8 || rest[0] != ' ' {
		return "", false
	}
	tz := rest[len(rest)-5:]
	if tz[0] != '+' && tz[0] != '-' {
		return "", false
	}
	for k := 1; k < 5; k++ {
		if tz[k] < '0' || tz[k] > '9' {
			return "", false
		}
	}
	if rest[len(rest)-6] != ' ' {
		return "", false
	}
	digits = rest[1 : len(rest)-6]
	if digits == "" {
		return "", false
	}
	for k := 0; k < len(digits); k++ {
		if digits[k] < '0' || digits[k] > '9' {
			return "", false
		}
	}
	return digits, true
}

func parseIdent(store *Store, line string) obj {
	name, email, digits, ok := matchIdent(line)
	if !ok {
		return obj{"name": line, "email": "", "date": store.ISO(1)}
	}
	sec, _ := strconv.ParseInt(digits, 10, 64)
	return obj{"name": name, "email": email, "date": isoSeconds(sec)}
}

func commitJSON(store *Store, repo *Repo, sha string, withFiles bool) obj {
	commit := GetCommit(repo, sha)
	if commit == nil {
		return nil
	}
	author := parseIdent(store, commit.Author)
	committer := parseIdent(store, commit.Committer)
	parents := make([]obj, 0, len(commit.Parents))
	for _, p := range commit.Parents {
		parents = append(parents, obj{
			"sha":      p,
			"url":      "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/commits/" + p,
			"html_url": "https://github.com/" + LOGIN + "/" + repo.Name + "/commit/" + p,
		})
	}
	out := obj{
		"sha":     sha,
		"node_id": GlobalID("Commit", sha),
		"commit": obj{
			"author":        author,
			"committer":     committer,
			"message":       commit.Message,
			"tree":          obj{"sha": commit.Tree, "url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/trees/" + commit.Tree},
			"url":           "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/commits/" + sha,
			"comment_count": 0,
			"verification":  obj{"verified": false, "reason": "unsigned", "signature": nil, "payload": nil},
		},
		"url":          "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/commits/" + sha,
		"html_url":     "https://github.com/" + LOGIN + "/" + repo.Name + "/commit/" + sha,
		"comments_url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/commits/" + sha + "/comments",
		"author":       userJSON(LOGIN, 1),
		"committer":    userJSON(LOGIN, 1),
		"parents":      parents,
	}
	if withFiles {
		var parentTree string
		if len(commit.Parents) > 0 {
			if pc := GetCommit(repo, commit.Parents[0]); pc != nil {
				parentTree = pc.Tree
			}
		}
		files := DiffTrees(repo, parentTree, commit.Tree)
		rendered := make([]obj, 0, len(files))
		additions, deletions := 0, 0
		for _, f := range files {
			entry := obj{
				"sha":          f.SHA,
				"filename":     f.Filename,
				"status":       f.Status,
				"additions":    f.Additions,
				"deletions":    f.Deletions,
				"changes":      f.Changes,
				"blob_url":     "https://github.com/" + LOGIN + "/" + repo.Name + "/blob/" + sha + "/" + f.Filename,
				"raw_url":      "https://github.com/" + LOGIN + "/" + repo.Name + "/raw/" + sha + "/" + f.Filename,
				"contents_url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/contents/" + f.Filename + "?ref=" + sha,
			}
			if f.Patch != nil {
				entry["patch"] = *f.Patch
			}
			rendered = append(rendered, entry)
			additions += f.Additions
			deletions += f.Deletions
		}
		out["files"] = rendered
		out["stats"] = obj{"total": additions + deletions, "additions": additions, "deletions": deletions}
	}
	return out
}

func listCommits(ctx *RestContext, repo *Repo) RestResult {
	refParam := queryOr(ctx.Query, "sha", "")
	if _, ok := firstQuery(ctx.Query, "sha"); !ok {
		refParam = queryOr(ctx.Query, "ref", repo.DefaultBranch)
	}
	tip := ResolveRef(repo, refParam)
	if tip == "" {
		return notFound()
	}
	start := ResolveToCommit(repo, tip)
	if start == "" {
		return notFound()
	}
	var order []string
	seen := make(map[string]bool)
	queue := []string{start}
	for qi := 0; qi < len(queue) && len(order) < 1000; qi++ {
		cur := queue[qi]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		order = append(order, cur)
		if c := GetCommit(repo, cur); c != nil {
			queue = append(queue, c.Parents...)
		}
	}
	startIdx, endIdx, pageNum, perPage := paginateLen(len(order), ctx.Query)
	items := make([]obj, 0, endIdx-startIdx)
	for _, sha := range order[startIdx:endIdx] {
		if item := commitJSON(ctx.Store, repo, sha, false); item != nil {
			items = append(items, item)
		}
	}
	return RestResult{Status: 200, JSON: items, Headers: linkHeader(ctx.Host, "/api/v3/repos/"+LOGIN+"/"+repo.Name+"/commits", ctx.Query, pageNum, perPage, len(order))}
}

func contentsEntry(store *Store, repo *Repo, path, name, sha string, size int, typ string) obj {
	full := LOGIN + "/" + repo.Name
	return obj{
		"type":         typ,
		"size":         size,
		"name":         name,
		"path":         path,
		"sha":          sha,
		"url":          "/api/v3/repos/" + full + "/contents/" + path,
		"git_url":      "/api/v3/repos/" + full + "/git/blobs/" + sha,
		"html_url":     "https://github.com/" + full + "/blob/HEAD/" + path,
		"download_url": "https://raw.githubusercontent.com/" + full + "/HEAD/" + path,
		"_links": obj{
			"git":  "/api/v3/repos/" + full + "/git/blobs/" + sha,
			"self": "/api/v3/repos/" + full + "/contents/" + path,
			"html": "https://github.com/" + full + "/blob/HEAD/" + path,
		},
	}
}

func baseName(path string) string {
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		return path[idx+1:]
	}
	return path
}

func getContents(ctx *RestContext, repo *Repo, path string) RestResult {
	refParam := queryOr(ctx.Query, "ref", repo.DefaultBranch)
	tip := ResolveRef(repo, refParam)
	if tip == "" {
		return notFound()
	}
	commitSHA := ResolveToCommit(repo, tip)
	if commitSHA == "" {
		return notFound()
	}
	commit := GetCommit(repo, commitSHA)
	if commit == nil {
		return notFound()
	}
	flat := FlattenTree(repo, commit.Tree)
	if direct, ok := flat[path]; ok {
		size := 0
		var content string
		if o := repo.Objects[direct.SHA]; o != nil {
			size = len(o.Data)
			content = base64.StdEncoding.EncodeToString(o.Data)
		}
		entry := contentsEntry(ctx.Store, repo, path, baseName(path), direct.SHA, size, "file")
		entry["encoding"] = "base64"
		entry["content"] = content
		return RestResult{Status: 200, JSON: entry}
	}
	prefix := ""
	if path != "" {
		prefix = path + "/"
	}
	type childInfo struct {
		sha   string
		mode  string
		isDir bool
		size  int
	}
	children := make(map[string]childInfo)
	for p, e := range flat {
		if prefix != "" && !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p
		if prefix != "" {
			rest = p[len(prefix):]
		}
		if rest == "" {
			continue
		}
		if slash := strings.Index(rest, "/"); slash == -1 {
			size := 0
			if o := repo.Objects[e.SHA]; o != nil {
				size = len(o.Data)
			}
			children[rest] = childInfo{sha: e.SHA, mode: e.Mode, size: size}
		} else {
			dirName := rest[:slash]
			if _, ok := children[dirName]; !ok {
				children[dirName] = childInfo{mode: "40000", isDir: true}
			}
		}
	}
	if len(children) == 0 {
		return notFound()
	}
	dirSHAs := make(map[string]string)
	var walkDirs func(treeSHA, base string)
	walkDirs = func(treeSHA, base string) {
		for _, e := range GetTree(repo, treeSHA) {
			if e.Kind != "tree" {
				continue
			}
			p := e.Name
			if base != "" {
				p = base + "/" + e.Name
			}
			dirSHAs[p] = e.SHA
			walkDirs(e.SHA, p)
		}
	}
	walkDirs(commit.Tree, "")
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]obj, 0, len(names))
	for _, name := range names {
		child := children[name]
		childPath := name
		if prefix != "" {
			childPath = prefix + name
		}
		sha := child.sha
		typ := "file"
		if child.isDir {
			sha = dirSHAs[childPath]
			typ = "dir"
		}
		out = append(out, contentsEntry(ctx.Store, repo, childPath, name, sha, child.size, typ))
	}
	return RestResult{Status: 200, JSON: out}
}

func identField(m map[string]any, key string) *string {
	if v, ok := m[key].(string); ok {
		return &v
	}
	return nil
}

func writeContents(ctx *RestContext, repo *Repo, path string) RestResult {
	body := bodyObject(ctx)
	message, _ := body["message"].(string)
	contentB64, hasContent := body["content"].(string)
	branch, _ := body["branch"].(string)
	if _, ok := body["branch"].(string); !ok {
		branch = repo.DefaultBranch
	}
	shaParam, _ := body["sha"].(string)
	hasSHA := false
	if _, ok := body["sha"].(string); ok {
		hasSHA = true
	}
	if !hasContent {
		return validationFailed([]validationError{{Resource: "Contents", Field: "content", Code: "missing"}}, "")
	}
	content, err := decodeBase64Lenient(contentB64)
	if err != nil {
		return validationFailed([]validationError{{Resource: "Contents", Field: "content", Code: "invalid"}}, "")
	}
	tip := ResolveRef(repo, branch)
	var parent string
	flat := make(map[string]FlatEntry)
	if tip != "" {
		commitSHA := ResolveToCommit(repo, tip)
		if commitSHA == "" {
			return notFound()
		}
		parent = commitSHA
		if commit := GetCommit(repo, commitSHA); commit != nil {
			for k, v := range FlattenTree(repo, commit.Tree) {
				flat[k] = v
			}
		}
	} else if len(repo.Refs) > 0 {
		// A contents write naming a branch that does not exist yet
		// starts that branch from the default branch tip.
		defaultTip, ok := repo.Refs["refs/heads/"+repo.DefaultBranch]
		if !ok {
			return notFound()
		}
		commitSHA := ResolveToCommit(repo, defaultTip)
		if commitSHA == "" {
			return notFound()
		}
		parent = commitSHA
		if commit := GetCommit(repo, commitSHA); commit != nil {
			for k, v := range FlattenTree(repo, commit.Tree) {
				flat[k] = v
			}
		}
	}
	existing, isUpdate := flat[path]
	if isUpdate {
		if !hasSHA {
			return validationFailed([]validationError{{Resource: "Contents", Field: "sha", Code: "missing"}}, `"sha" wasn't supplied.`)
		}
		if shaParam != existing.SHA {
			return errResult(409, "The content has changed since the sha was fetched, or the sha is invalid.", nil)
		}
	}
	blobSHA := StoreObject(repo, ObjectBlob, content)
	mode := "100644"
	if isUpdate {
		mode = existing.Mode
	}
	flat[path] = FlatEntry{SHA: blobSHA, Mode: mode}
	treeSHA := TreeFromFlat(repo, flat)
	seq := ctx.Store.NextSeq()
	var authorIdent, committerIdent string
	if committer, ok := body["committer"].(map[string]any); ok {
		committerIdent = gitIdentity(ctx.Store, seq, identField(committer, "name"), identField(committer, "email"))
	} else {
		committerIdent = gitIdentity(ctx.Store, seq, nil, nil)
	}
	if author, ok := body["author"].(map[string]any); ok {
		authorIdent = gitIdentity(ctx.Store, seq, identField(author, "name"), identField(author, "email"))
	} else {
		authorIdent = gitIdentity(ctx.Store, seq, nil, nil)
	}
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	commitSHA := BuildCommit(repo, treeSHA, parents, authorIdent, committerIdent, message)
	repo.Refs["refs/heads/"+branch] = commitSHA
	repo.UpdatedSeq = seq
	repo.Touch()
	entry := contentsEntry(ctx.Store, repo, path, baseName(path), blobSHA, len(content), "file")
	entry["encoding"] = "base64"
	entry["content"] = base64.StdEncoding.EncodeToString(content)
	status := 201
	if isUpdate {
		status = 200
	}
	return RestResult{Status: status, JSON: obj{"content": entry, "commit": commitJSON(ctx.Store, repo, commitSHA, false)}}
}

func deleteContents(ctx *RestContext, repo *Repo, path string) RestResult {
	body := bodyObject(ctx)
	message, _ := body["message"].(string)
	branch, _ := body["branch"].(string)
	if _, ok := body["branch"].(string); !ok {
		branch = repo.DefaultBranch
	}
	shaParam, hasSHA := body["sha"].(string)
	tip := ResolveRef(repo, branch)
	if tip == "" {
		return notFound()
	}
	commitSHA := ResolveToCommit(repo, tip)
	if commitSHA == "" {
		return notFound()
	}
	commit := GetCommit(repo, commitSHA)
	if commit == nil {
		return notFound()
	}
	flat := make(map[string]FlatEntry)
	for k, v := range FlattenTree(repo, commit.Tree) {
		flat[k] = v
	}
	existing, ok := flat[path]
	if !ok {
		return notFound()
	}
	if !hasSHA {
		return validationFailed([]validationError{{Resource: "Contents", Field: "sha", Code: "missing"}}, `"sha" wasn't supplied.`)
	}
	if shaParam != existing.SHA {
		return errResult(409, "The content has changed since the sha was fetched, or the sha is invalid.", nil)
	}
	delete(flat, path)
	treeSHA := TreeFromFlat(repo, flat)
	seq := ctx.Store.NextSeq()
	ident := gitIdentity(ctx.Store, seq, nil, nil)
	newCommit := BuildCommit(repo, treeSHA, []string{commitSHA}, ident, ident, message)
	repo.Refs["refs/heads/"+branch] = newCommit
	repo.UpdatedSeq = seq
	repo.Touch()
	return RestResult{Status: 200, JSON: obj{"content": nil, "commit": commitJSON(ctx.Store, repo, newCommit, false)}}
}

type issueRow struct {
	issue *Issue
	pull  *Pull
}

func pseudoIssue(pull *Pull) *Issue {
	return &Issue{
		ID:         pull.ID,
		Number:     pull.Number,
		Title:      pull.Title,
		Body:       pull.Body,
		State:      pull.State,
		Labels:     []string{},
		User:       pull.User,
		CreatedSeq: pull.CreatedSeq,
		UpdatedSeq: pull.UpdatedSeq,
		ClosedSeq:  pull.ClosedSeq,
		Comments:   pull.Comments,
	}
}

func listIssues(ctx *RestContext, repo *Repo) RestResult {
	state := queryOr(ctx.Query, "state", "open")
	sortKey := queryOr(ctx.Query, "sort", "created")
	direction := queryOr(ctx.Query, "direction", "desc")
	asc := direction == "asc"
	var rows []issueRow
	for _, issue := range repo.Issues {
		rows = append(rows, issueRow{issue: issue})
	}
	for _, pull := range repo.Pulls {
		rows = append(rows, issueRow{issue: pseudoIssue(pull), pull: pull})
	}
	filtered := rows
	if state != "all" {
		filtered = nil
		for _, r := range rows {
			if string(r.issue.State) == state {
				filtered = append(filtered, r)
			}
		}
	}
	keyOf := func(r issueRow) int {
		switch sortKey {
		case "updated":
			return r.issue.UpdatedSeq
		case "comments":
			return len(r.issue.Comments)
		default:
			return r.issue.CreatedSeq
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		d := keyOf(filtered[i]) - keyOf(filtered[j])
		if d != 0 {
			if asc {
				return d < 0
			}
			return d > 0
		}
		if asc {
			return filtered[i].issue.Number < filtered[j].issue.Number
		}
		return filtered[i].issue.Number > filtered[j].issue.Number
	})
	start, end, pageNum, perPage := paginateLen(len(filtered), ctx.Query)
	items := make([]obj, 0, end-start)
	for _, r := range filtered[start:end] {
		items = append(items, IssueJSON(ctx.Store, repo, r.issue, r.pull))
	}
	return RestResult{Status: 200, JSON: items, Headers: linkHeader(ctx.Host, "/api/v3/repos/"+LOGIN+"/"+repo.Name+"/issues", ctx.Query, pageNum, perPage, len(filtered))}
}

func getIssue(ctx *RestContext, repo *Repo, number int) RestResult {
	cacheKey := "issue:" + strconv.Itoa(number) + ":v" + strconv.Itoa(repo.Version)
	if v, ok := repo.jsonCache.Load(cacheKey); ok {
		return RestResult{Status: 200, Raw: v.([]byte)}
	}
	render := func(issue *Issue, pull *Pull) RestResult {
		text, err := marshalJSON(IssueJSON(ctx.Store, repo, issue, pull))
		if err != nil {
			return errResult(500, err.Error(), nil)
		}
		repo.jsonCache.Store(cacheKey, text)
		return RestResult{Status: 200, Raw: text}
	}
	if issue, ok := repo.IssueByNumber[number]; ok {
		return render(issue, nil)
	}
	if pull, ok := repo.PullByNumber[number]; ok {
		return render(pseudoIssue(pull), pull)
	}
	return notFound()
}

func listPulls(ctx *RestContext, repo *Repo) RestResult {
	state := queryOr(ctx.Query, "state", "open")
	sortKey := queryOr(ctx.Query, "sort", "created")
	direction := queryOr(ctx.Query, "direction", "desc")
	asc := direction == "asc"
	filtered := append([]*Pull(nil), repo.Pulls...)
	if state != "all" {
		filtered = nil
		for _, p := range repo.Pulls {
			if string(p.State) == state {
				filtered = append(filtered, p)
			}
		}
	}
	keyOf := func(p *Pull) int {
		if sortKey == "updated" {
			return p.UpdatedSeq
		}
		return p.CreatedSeq
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		d := keyOf(filtered[i]) - keyOf(filtered[j])
		if d != 0 {
			if asc {
				return d < 0
			}
			return d > 0
		}
		if asc {
			return filtered[i].Number < filtered[j].Number
		}
		return filtered[i].Number > filtered[j].Number
	})
	start, end, pageNum, perPage := paginateLen(len(filtered), ctx.Query)
	items := make([]obj, 0, end-start)
	for _, p := range filtered[start:end] {
		items = append(items, PRJSON(ctx.Store, repo, p))
	}
	return RestResult{Status: 200, JSON: items, Headers: linkHeader(ctx.Host, "/api/v3/repos/"+LOGIN+"/"+repo.Name+"/pulls", ctx.Query, pageNum, perPage, len(filtered))}
}

func listComments(ctx *RestContext, repo *Repo, number int) RestResult {
	var comments []*Comment
	if issue, ok := repo.IssueByNumber[number]; ok {
		comments = issue.Comments
	} else if pull, ok := repo.PullByNumber[number]; ok {
		comments = pull.Comments
	} else {
		return notFound()
	}
	start, end, pageNum, perPage := paginateLen(len(comments), ctx.Query)
	items := make([]obj, 0, end-start)
	for _, c := range comments[start:end] {
		items = append(items, CommentJSON(ctx.Store, repo, number, c))
	}
	return RestResult{Status: 200, JSON: items, Headers: linkHeader(ctx.Host, "/api/v3/repos/"+LOGIN+"/"+repo.Name+"/issues/"+strconv.Itoa(number)+"/comments", ctx.Query, pageNum, perPage, len(comments))}
}

func createComment(ctx *RestContext, repo *Repo, number int) RestResult {
	issue, hasIssue := repo.IssueByNumber[number]
	pull, hasPull := repo.PullByNumber[number]
	if !hasIssue && !hasPull {
		return notFound()
	}
	body := bodyObject(ctx)
	text, _ := body["body"].(string)
	if _, ok := body["body"].(string); !ok || text == "" {
		return validationFailed([]validationError{{Resource: "IssueComment", Field: "body", Code: "missing"}}, "")
	}
	seq := ctx.Store.NextSeq()
	comment := &Comment{ID: ctx.Store.NextID(), Body: text, User: LOGIN, CreatedSeq: seq, UpdatedSeq: seq}
	if hasIssue {
		issue.Comments = append(issue.Comments, comment)
		issue.UpdatedSeq = seq
	} else {
		pull.Comments = append(pull.Comments, comment)
		pull.UpdatedSeq = seq
	}
	repo.Touch()
	return RestResult{Status: 201, JSON: CommentJSON(ctx.Store, repo, number, comment)}
}

func findComment(repo *Repo, id int) (*Comment, int, bool) {
	for _, issue := range repo.Issues {
		for _, c := range issue.Comments {
			if c.ID == id {
				return c, issue.Number, true
			}
		}
	}
	for _, pull := range repo.Pulls {
		for _, c := range pull.Comments {
			if c.ID == id {
				return c, pull.Number, true
			}
		}
	}
	return nil, 0, false
}

// ---------------------------------------------------------------------------
// Main REST dispatcher
// ---------------------------------------------------------------------------

// HandleRest routes one REST v3 request.
func HandleRest(ctx *RestContext) RestResult {
	seg := ctx.Segments
	method := ctx.Method

	// API root.
	if len(seg) == 0 {
		if method != "GET" {
			return notFound()
		}
		return RestResult{Status: 200, JSON: obj{
			"current_user_url":                     "/api/v3/user",
			"current_user_authorizations_html_url": "https://github.com/settings/applications/authorizations",
			"authorizations_url":                   "/api/v3/authorizations",
			"code_search_url":                      "/api/v3/search/code{?q}",
			"commit_search_url":                    "/api/v3/search/commits{?q}",
			"emails_url":                           "/api/v3/user/emails",
			"emojis_url":                           "/api/v3/emojis",
			"events_url":                           "/api/v3/events",
			"feeds_url":                            "/api/v3/feeds",
			"followers_url":                        "/api/v3/user/followers",
			"following_url":                        "/api/v3/user/following{/target}",
			"gists_url":                            "/api/v3/gists{/gist_id}",
			"hub_url":                              "/api/v3/hub",
			"issue_search_url":                     "/api/v3/search/issues{?q}",
			"issues_url":                           "/api/v3/issues",
			"keys_url":                             "/api/v3/user/keys",
			"label_search_url":                     "/api/v3/search/labels{?q}",
			"notifications_url":                    "/api/v3/notifications",
			"organization_url":                     "/api/v3/orgs/{org}",
			"organization_repositories_url":        "/api/v3/orgs/{org}/repos{?type,page,per_page,sort}",
			"organization_teams_url":               "/api/v3/orgs/{org}/teams",
			"public_gists_url":                     "/api/v3/gists/public",
			"rate_limit_url":                       "/api/v3/rate_limit",
			"repository_url":                       "/api/v3/repos/{owner}/{repo}",
			"repository_search_url":                "/api/v3/search/repositories{?q}",
			"current_user_repositories_url":        "/api/v3/user/repos{?type,page,per_page,sort}",
			"starred_url":                          "/api/v3/user/starred{/owner}{/repo}",
			"starred_gists_url":                    "/api/v3/gists/starred",
			"topic_search_url":                     "/api/v3/search/topics{?q}",
			"user_url":                             "/api/v3/users/{user}",
			"user_organizations_url":               "/api/v3/user/orgs",
			"user_repositories_url":                "/api/v3/users/{user}/repos{?type,page,per_page,sort}",
			"user_search_url":                      "/api/v3/search/users{?q}",
		}, Headers: map[string]string{"x-oauth-scopes": "repo, read:org"}}
	}

	if seg[0] == "meta" && len(seg) == 1 && method == "GET" {
		return RestResult{Status: 200, JSON: obj{
			"verifiable_password_authentication": true,
			"ssh_key_fingerprints":               obj{},
			"ssh_keys":                           []any{},
			"hooks":                              []any{},
			"web":                                []any{},
			"api":                                []any{},
			"git":                                []any{},
			"packages":                           []any{},
			"pages":                              []any{},
			"importer":                           []any{},
			"actions":                            []any{},
			"dependabot":                         []any{},
			"installed_version":                  "1.0.0",
		}}
	}

	if seg[0] == "rate_limit" && len(seg) == 1 && method == "GET" {
		reset := ctx.Store.BaseMs/1000 + 3600
		return RestResult{Status: 200, JSON: obj{
			"resources": obj{
				"core":                 obj{"limit": 5000, "remaining": 4999, "reset": reset, "used": 1},
				"graphql":              obj{"limit": 5000, "remaining": 4999, "reset": reset, "used": 1},
				"integration_manifest": obj{"limit": 5000, "remaining": 4999, "reset": reset, "used": 1},
				"search":               obj{"limit": 30, "remaining": 30, "reset": reset, "used": 0},
			},
			"rate": obj{"limit": 5000, "remaining": 4999, "reset": reset, "used": 1},
		}}
	}

	if seg[0] == "user" && len(seg) == 1 && method == "GET" {
		user := userJSON(LOGIN, 1)
		user["name"] = LOGIN
		user["email"] = nil
		user["bio"] = nil
		user["public_repos"] = len(ctx.Store.Repos)
		user["public_gists"] = 0
		user["followers"] = 0
		user["following"] = 0
		user["created_at"] = ctx.Store.ISO(1)
		user["updated_at"] = ctx.Store.ISO(1)
		return RestResult{Status: 200, JSON: user}
	}

	if seg[0] == "user" && len(seg) == 2 && seg[1] == "repos" {
		if method == "GET" {
			return listRepos(ctx)
		}
		if method == "POST" {
			return createRepo(ctx)
		}
		return notFound()
	}

	if seg[0] == "users" && len(seg) == 2 && method == "GET" {
		if seg[1] != LOGIN {
			return notFound()
		}
		return RestResult{Status: 200, JSON: userJSON(LOGIN, 1)}
	}

	if seg[0] != "repos" || len(seg) < 3 {
		return notFound()
	}
	owner, repoName := seg[1], seg[2]
	repo, repoErr := requireRepo(ctx, owner, repoName)
	if repoErr != nil {
		return *repoErr
	}
	rest := seg[3:]

	// /repos/{owner}/{repo}
	if len(rest) == 0 {
		switch method {
		case "GET":
			return RestResult{Status: 200, JSON: RepoJSON(ctx.Store, repo)}
		case "PATCH":
			body := bodyObject(ctx)
			seq := ctx.Store.NextSeq()
			if v, ok := body["description"]; ok {
				if s, isStr := v.(string); isStr {
					repo.Description = s
				} else if v == nil {
					repo.Description = ""
				}
			}
			if b, ok := body["private"].(bool); ok {
				repo.IsPrivate = b
			}
			if s, ok := body["default_branch"].(string); ok {
				if _, exists := repo.Refs["refs/heads/"+s]; exists {
					repo.DefaultBranch = s
				}
			}
			if s, ok := body["name"].(string); ok && s != "" && s != repo.Name {
				if ctx.Store.GetRepo(s) != nil {
					return validationFailed([]validationError{{Resource: "Repository", Field: "name", Code: "already_exists"}}, "")
				}
				delete(ctx.Store.Repos, repo.Name)
				repo.Name = s
				ctx.Store.Repos[s] = repo
			}
			repo.UpdatedSeq = seq
			repo.Touch()
			return RestResult{Status: 200, JSON: RepoJSON(ctx.Store, repo)}
		case "DELETE":
			ctx.Store.DeleteRepo(repo.Name)
			return RestResult{Status: 204, JSON: nil}
		default:
			return notFound()
		}
	}

	resource := rest[0]

	if resource == "branches" {
		if len(rest) == 1 && method == "GET" {
			return listBranches(ctx, repo)
		}
		if len(rest) >= 2 && method == "GET" {
			return getBranch(ctx, repo, strings.Join(rest[1:], "/"))
		}
		return notFound()
	}

	if resource == "commits" {
		if len(rest) == 1 && method == "GET" {
			return listCommits(ctx, repo)
		}
		if len(rest) == 2 && method == "GET" {
			sha := ResolveRef(repo, rest[1])
			if sha == "" {
				return notFound()
			}
			commitSHA := ResolveToCommit(repo, sha)
			if commitSHA == "" {
				return notFound()
			}
			item := commitJSON(ctx.Store, repo, commitSHA, true)
			if item == nil {
				return notFound()
			}
			return RestResult{Status: 200, JSON: item}
		}
		return notFound()
	}

	if resource == "contents" {
		path := strings.Join(rest[1:], "/")
		switch method {
		case "GET":
			return getContents(ctx, repo, path)
		case "PUT":
			return writeContents(ctx, repo, path)
		case "DELETE":
			return deleteContents(ctx, repo, path)
		default:
			return notFound()
		}
	}

	if resource == "labels" {
		if len(rest) == 1 {
			if method == "GET" {
				items := make([]obj, 0, len(repo.Labels))
				for _, l := range repo.Labels {
					items = append(items, LabelJSON(ctx.Store, repo, l))
				}
				// Sort by id for deterministic output: labels were
				// created in id order in the reference iteration.
				sort.Slice(items, func(i, j int) bool { return items[i]["id"].(int) < items[j]["id"].(int) })
				return RestResult{Status: 200, JSON: items}
			}
			if method == "POST" {
				body := bodyObject(ctx)
				name, ok := body["name"].(string)
				if !ok || name == "" {
					return validationFailed([]validationError{{Resource: "Label", Field: "name", Code: "missing"}}, "")
				}
				if _, exists := repo.Labels[name]; exists {
					return validationFailed([]validationError{{Resource: "Label", Field: "name", Code: "already_exists"}}, "")
				}
				color := "ededed"
				if s, ok := body["color"].(string); ok {
					color = strings.TrimPrefix(s, "#")
				}
				description, _ := body["description"].(string)
				label := &Label{ID: ctx.Store.NextID(), Name: name, Color: color, Description: description, Seq: ctx.Store.NextSeq()}
				repo.Labels[name] = label
				repo.Touch()
				return RestResult{Status: 201, JSON: LabelJSON(ctx.Store, repo, label)}
			}
			return notFound()
		}
		if len(rest) == 2 {
			label, ok := repo.Labels[rest[1]]
			if !ok {
				return notFound()
			}
			switch method {
			case "GET":
				return RestResult{Status: 200, JSON: LabelJSON(ctx.Store, repo, label)}
			case "PATCH":
				body := bodyObject(ctx)
				if s, ok := body["name"].(string); ok && s != "" && s != label.Name {
					delete(repo.Labels, label.Name)
					label.Name = s
					repo.Labels[label.Name] = label
				}
				if s, ok := body["color"].(string); ok {
					label.Color = strings.TrimPrefix(s, "#")
				}
				if s, ok := body["description"].(string); ok {
					label.Description = s
				}
				repo.Touch()
				return RestResult{Status: 200, JSON: LabelJSON(ctx.Store, repo, label)}
			case "DELETE":
				delete(repo.Labels, label.Name)
				repo.Touch()
				return RestResult{Status: 204, JSON: nil}
			default:
				return notFound()
			}
		}
		return notFound()
	}

	if resource == "issues" {
		return handleIssues(ctx, repo, rest)
	}

	if resource == "pulls" {
		return handlePulls(ctx, repo, rest)
	}

	if resource == "git" {
		return handleGit(ctx, repo, rest)
	}

	return notFound()
}

func handleIssues(ctx *RestContext, repo *Repo, rest []string) RestResult {
	method := ctx.Method
	if len(rest) == 1 {
		if method == "GET" {
			return listIssues(ctx, repo)
		}
		if method == "POST" {
			body := bodyObject(ctx)
			title, ok := body["title"].(string)
			if !ok || title == "" {
				return validationFailed([]validationError{{Resource: "Issue", Field: "title", Code: "missing"}}, "")
			}
			rawLabels, present := body["labels"]
			labelNames, valid := ParseLabelNames(rawLabels, present)
			if !valid {
				return validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "invalid"}}, "")
			}
			var bodyText *string
			if s, ok := body["body"].(string); ok {
				bodyText = &s
			}
			created, errRes := CreateIssueRecord(ctx.Store, repo, title, bodyText, labelNames)
			if errRes != nil {
				return *errRes
			}
			return RestResult{Status: 201, JSON: IssueJSON(ctx.Store, repo, created, nil)}
		}
		return notFound()
	}
	if len(rest) == 2 && rest[1] == "comments" && method == "GET" {
		var all []obj
		for _, issue := range repo.Issues {
			for _, c := range issue.Comments {
				all = append(all, CommentJSON(ctx.Store, repo, issue.Number, c))
			}
		}
		if all == nil {
			all = []obj{}
		}
		return RestResult{Status: 200, JSON: all}
	}
	if len(rest) == 3 && rest[1] == "comments" {
		id, err := strconv.Atoi(rest[2])
		if err != nil {
			return notFound()
		}
		comment, ownerNumber, found := findComment(repo, id)
		if !found {
			return notFound()
		}
		switch method {
		case "GET":
			return RestResult{Status: 200, JSON: CommentJSON(ctx.Store, repo, ownerNumber, comment)}
		case "PATCH":
			body := bodyObject(ctx)
			if s, ok := body["body"].(string); ok {
				comment.Body = s
			}
			comment.UpdatedSeq = ctx.Store.NextSeq()
			repo.Touch()
			return RestResult{Status: 200, JSON: CommentJSON(ctx.Store, repo, ownerNumber, comment)}
		case "DELETE":
			for _, issue := range repo.Issues {
				for i, c := range issue.Comments {
					if c.ID == id {
						issue.Comments = append(issue.Comments[:i], issue.Comments[i+1:]...)
						break
					}
				}
			}
			for _, pull := range repo.Pulls {
				for i, c := range pull.Comments {
					if c.ID == id {
						pull.Comments = append(pull.Comments[:i], pull.Comments[i+1:]...)
						break
					}
				}
			}
			repo.Touch()
			return RestResult{Status: 204, JSON: nil}
		default:
			return notFound()
		}
	}
	number, err := strconv.Atoi(rest[1])
	if err != nil {
		return notFound()
	}
	if len(rest) == 2 {
		switch method {
		case "GET":
			return getIssue(ctx, repo, number)
		case "PATCH":
			issue, hasIssue := repo.IssueByNumber[number]
			pull, hasPull := repo.PullByNumber[number]
			if !hasIssue && !hasPull {
				return notFound()
			}
			body := bodyObject(ctx)
			if hasIssue {
				if s, ok := body["title"].(string); ok {
					issue.Title = s
				}
				if v, ok := body["body"]; ok {
					if s, isStr := v.(string); isStr {
						issue.Body = &s
					} else if v == nil {
						issue.Body = nil
					}
				}
				if st, ok := body["state"].(string); ok && (st == "open" || st == "closed") {
					issue.State = IssueState(st)
					if st == "closed" {
						seq := ctx.Store.NextSeq()
						issue.ClosedSeq = &seq
					} else {
						issue.ClosedSeq = nil
					}
				}
				if _, present := body["labels"]; present {
					labelNames, valid := ParseLabelNames(body["labels"], true)
					if !valid {
						return validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "invalid"}}, "")
					}
					for _, name := range labelNames {
						if _, ok := repo.Labels[name]; !ok {
							return validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "custom"}}, "")
						}
					}
					issue.Labels = append([]string(nil), labelNames...)
				}
				issue.UpdatedSeq = ctx.Store.NextSeq()
				repo.Touch()
				return RestResult{Status: 200, JSON: IssueJSON(ctx.Store, repo, issue, nil)}
			}
			if s, ok := body["title"].(string); ok {
				pull.Title = s
			}
			if v, ok := body["body"]; ok {
				if s, isStr := v.(string); isStr {
					pull.Body = &s
				} else if v == nil {
					pull.Body = nil
				}
			}
			if st, ok := body["state"].(string); ok && (st == "open" || st == "closed") {
				pull.State = IssueState(st)
				if st == "closed" {
					seq := ctx.Store.NextSeq()
					pull.ClosedSeq = &seq
				} else {
					pull.ClosedSeq = nil
				}
			}
			pull.UpdatedSeq = ctx.Store.NextSeq()
			repo.Touch()
			return RestResult{Status: 200, JSON: PRJSON(ctx.Store, repo, pull)}
		default:
			return notFound()
		}
	}
	if len(rest) == 3 && rest[2] == "comments" {
		if method == "GET" {
			return listComments(ctx, repo, number)
		}
		if method == "POST" {
			return createComment(ctx, repo, number)
		}
		return notFound()
	}
	if len(rest) == 3 && rest[2] == "labels" {
		issue, ok := repo.IssueByNumber[number]
		if !ok {
			return notFound()
		}
		switch method {
		case "GET":
			return RestResult{Status: 200, JSON: labelsJSON(ctx.Store, repo, issue.Labels)}
		case "POST":
			var names []string
			var valid bool
			if arr, isArr := ctx.Body.([]any); isArr {
				names, valid = ParseLabelNames(arr, true)
			} else if m, isMap := ctx.Body.(map[string]any); isMap {
				raw, present := m["labels"]
				names, valid = ParseLabelNames(raw, present)
			} else {
				names, valid = ParseLabelNames(nil, false)
			}
			if !valid {
				return validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "invalid"}}, "")
			}
			for _, name := range names {
				if _, ok := repo.Labels[name]; !ok {
					return validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "custom"}}, "")
				}
				found := false
				for _, existing := range issue.Labels {
					if existing == name {
						found = true
						break
					}
				}
				if !found {
					issue.Labels = append(issue.Labels, name)
				}
			}
			repo.Touch()
			return RestResult{Status: 200, JSON: labelsJSON(ctx.Store, repo, issue.Labels)}
		case "PUT":
			names, valid := ParseLabelNames(ctx.Body, ctx.HasBody)
			if !valid {
				return validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "invalid"}}, "")
			}
			for _, name := range names {
				if _, ok := repo.Labels[name]; !ok {
					return validationFailed([]validationError{{Resource: "Issue", Field: "labels", Code: "custom"}}, "")
				}
			}
			issue.Labels = append([]string(nil), names...)
			repo.Touch()
			return RestResult{Status: 200, JSON: labelsJSON(ctx.Store, repo, issue.Labels)}
		default:
			return notFound()
		}
	}
	if len(rest) == 4 && rest[2] == "labels" && method == "DELETE" {
		issue, ok := repo.IssueByNumber[number]
		if !ok {
			return notFound()
		}
		filtered := issue.Labels[:0]
		for _, l := range issue.Labels {
			if l != rest[3] {
				filtered = append(filtered, l)
			}
		}
		issue.Labels = filtered
		repo.Touch()
		return RestResult{Status: 200, JSON: labelsJSON(ctx.Store, repo, issue.Labels)}
	}
	return notFound()
}

func handlePulls(ctx *RestContext, repo *Repo, rest []string) RestResult {
	method := ctx.Method
	if len(rest) == 1 {
		if method == "GET" {
			return listPulls(ctx, repo)
		}
		if method == "POST" {
			body := bodyObject(ctx)
			title, ok := body["title"].(string)
			if !ok || title == "" {
				return validationFailed([]validationError{{Resource: "PullRequest", Field: "title", Code: "missing"}}, "")
			}
			head, headOK := body["head"].(string)
			base, baseOK := body["base"].(string)
			if !headOK || head == "" || !baseOK || base == "" {
				return validationFailed([]validationError{{Resource: "PullRequest", Field: "head", Code: "missing"}}, "")
			}
			var bodyText *string
			if s, ok := body["body"].(string); ok {
				bodyText = &s
			}
			created, errRes := CreatePullRecord(ctx.Store, repo, title, bodyText, head, base)
			if errRes != nil {
				return *errRes
			}
			return RestResult{Status: 201, JSON: PRJSON(ctx.Store, repo, created)}
		}
		return notFound()
	}
	number, err := strconv.Atoi(rest[1])
	if err != nil {
		return notFound()
	}
	pull, hasPull := repo.PullByNumber[number]
	if len(rest) == 2 {
		if !hasPull {
			return notFound()
		}
		switch method {
		case "GET":
			return RestResult{Status: 200, JSON: PRJSON(ctx.Store, repo, pull)}
		case "PATCH":
			body := bodyObject(ctx)
			if s, ok := body["title"].(string); ok {
				pull.Title = s
			}
			if v, ok := body["body"]; ok {
				if s, isStr := v.(string); isStr {
					pull.Body = &s
				} else if v == nil {
					pull.Body = nil
				}
			}
			if st, ok := body["state"].(string); ok && (st == "open" || st == "closed") {
				pull.State = IssueState(st)
				if st == "closed" {
					seq := ctx.Store.NextSeq()
					pull.ClosedSeq = &seq
				} else {
					pull.ClosedSeq = nil
				}
			}
			pull.UpdatedSeq = ctx.Store.NextSeq()
			repo.Touch()
			return RestResult{Status: 200, JSON: PRJSON(ctx.Store, repo, pull)}
		default:
			return notFound()
		}
	}
	if len(rest) == 3 && rest[2] == "files" && method == "GET" {
		if !hasPull {
			return notFound()
		}
		info := PRMergeInfo(ctx.Store, repo, pull)
		start, end, pageNum, perPage := paginateLen(len(info.Files), ctx.Query)
		items := make([]obj, 0, end-start)
		for _, f := range info.Files[start:end] {
			entry := obj{
				"sha":          f.SHA,
				"filename":     f.Filename,
				"status":       f.Status,
				"additions":    f.Additions,
				"deletions":    f.Deletions,
				"changes":      f.Changes,
				"blob_url":     "https://github.com/" + LOGIN + "/" + repo.Name + "/blob/" + pull.HeadSHA + "/" + f.Filename,
				"raw_url":      "https://github.com/" + LOGIN + "/" + repo.Name + "/raw/" + pull.HeadSHA + "/" + f.Filename,
				"contents_url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/contents/" + f.Filename + "?ref=" + pull.HeadSHA,
			}
			if f.Patch != nil {
				entry["patch"] = *f.Patch
			}
			items = append(items, entry)
		}
		return RestResult{Status: 200, JSON: items, Headers: linkHeader(ctx.Host, "/api/v3/repos/"+LOGIN+"/"+repo.Name+"/pulls/"+strconv.Itoa(number)+"/files", ctx.Query, pageNum, perPage, len(info.Files))}
	}
	if len(rest) == 3 && rest[2] == "merge" {
		if method == "GET" {
			if !hasPull {
				return notFound()
			}
			if pull.Merged {
				return RestResult{Status: 204, JSON: nil}
			}
			return notFound()
		}
		if method == "PUT" {
			if !hasPull {
				return notFound()
			}
			body := bodyObject(ctx)
			var title, message *string
			if s, ok := body["commit_title"].(string); ok {
				title = &s
			}
			if s, ok := body["commit_message"].(string); ok {
				message = &s
			}
			sha, mergeErr := MergePullRecord(ctx.Store, repo, pull, title, message)
			if mergeErr != "" {
				if mergeErr == MergeErrConflict {
					return errResult(405, "Pull request is not mergeable: merge conflict", nil)
				}
				return errResult(405, "Pull request has already been merged", nil)
			}
			return RestResult{Status: 200, JSON: obj{"sha": sha, "merged": true, "message": "Pull request successfully merged"}}
		}
		return notFound()
	}
	if len(rest) == 3 && rest[2] == "comments" {
		if !hasPull {
			return notFound()
		}
		if method == "GET" {
			return RestResult{Status: 200, JSON: []any{}}
		}
		if method == "POST" {
			body := bodyObject(ctx)
			text, ok := body["body"].(string)
			if !ok || text == "" {
				return validationFailed([]validationError{{Resource: "PullRequestReviewComment", Field: "body", Code: "missing"}}, "")
			}
			seq := ctx.Store.NextSeq()
			comment := &Comment{ID: ctx.Store.NextID(), Body: text, User: LOGIN, CreatedSeq: seq, UpdatedSeq: seq}
			pull.Comments = append(pull.Comments, comment)
			repo.Touch()
			rendered := CommentJSON(ctx.Store, repo, number, comment)
			var path any
			if s, ok := body["path"].(string); ok {
				path = s
			}
			rendered["path"] = path
			rendered["position"] = nil
			rendered["commit_id"] = pull.HeadSHA
			rendered["original_commit_id"] = pull.HeadSHA
			rendered["diff_hunk"] = ""
			return RestResult{Status: 201, JSON: rendered}
		}
		return notFound()
	}
	if len(rest) == 3 && rest[2] == "commits" && method == "GET" {
		if !hasPull {
			return notFound()
		}
		// Warm the merge-info cache, as the reference does; the commits
		// below are computed from the ref tips directly.
		_ = PRMergeInfo(ctx.Store, repo, pull)
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
		var order []string
		seen := make(map[string]bool)
		queue := []string{pull.HeadSHA}
		for qi := 0; qi < len(queue); qi++ {
			cur := queue[qi]
			if seen[cur] || baseAncestors[cur] {
				continue
			}
			seen[cur] = true
			order = append(order, cur)
			if c := GetCommit(repo, cur); c != nil {
				queue = append(queue, c.Parents...)
			}
		}
		items := make([]obj, 0, len(order))
		for _, sha := range order {
			if item := commitJSON(ctx.Store, repo, sha, false); item != nil {
				items = append(items, item)
			}
		}
		return RestResult{Status: 200, JSON: items}
	}
	return notFound()
}

func handleGit(ctx *RestContext, repo *Repo, rest []string) RestResult {
	method := ctx.Method
	if len(rest) >= 2 && rest[1] == "refs" {
		refObject := func(ref, sha string) obj {
			objType := "commit"
			if o := repo.Objects[sha]; o != nil {
				objType = string(o.Type)
			}
			return obj{
				"ref":     ref,
				"node_id": GlobalID("Ref", sha),
				"url":     "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/refs/" + strings.TrimPrefix(ref, "refs/"),
				"object":  obj{"sha": sha, "type": objType, "url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/commits/" + sha},
			}
		}
		if len(rest) == 2 {
			if method == "GET" {
				refs := sortedRefs(repo)
				out := make([]obj, 0, len(refs))
				for _, r := range refs {
					out = append(out, refObject(r[0], r[1]))
				}
				return RestResult{Status: 200, JSON: out}
			}
			if method == "POST" {
				body := bodyObject(ctx)
				ref, refOK := body["ref"].(string)
				sha, shaOK := body["sha"].(string)
				if !refOK || !strings.HasPrefix(ref, "refs/") {
					return validationFailed([]validationError{{Resource: "Reference", Field: "ref", Code: "invalid"}}, "")
				}
				if !shaOK || repo.Objects[sha] == nil {
					return validationFailed([]validationError{{Resource: "Reference", Field: "sha", Code: "invalid"}}, "")
				}
				if _, exists := repo.Refs[ref]; exists {
					return validationFailed([]validationError{{Resource: "Reference", Field: "ref", Code: "already_exists"}}, "")
				}
				repo.Refs[ref] = sha
				repo.Touch()
				return RestResult{Status: 201, JSON: refObject(ref, sha)}
			}
			return notFound()
		}
		if len(rest) >= 3 {
			refName := "refs/" + strings.Join(rest[2:], "/")
			if method == "GET" {
				sha, ok := repo.Refs[refName]
				if !ok {
					return notFound()
				}
				return RestResult{Status: 200, JSON: refObject(refName, sha)}
			}
			if method == "PATCH" {
				sha, ok := repo.Refs[refName]
				if !ok {
					return notFound()
				}
				body := bodyObject(ctx)
				newSHA, shaOK := body["sha"].(string)
				if !shaOK || repo.Objects[newSHA] == nil {
					return validationFailed([]validationError{{Resource: "Reference", Field: "sha", Code: "invalid"}}, "")
				}
				force := body["force"] == true
				if !force && newSHA != sha {
					oldCommit := ResolveToCommit(repo, sha)
					newCommit := ResolveToCommit(repo, newSHA)
					if oldCommit == "" || newCommit == "" || !IsAncestor(repo, oldCommit, newCommit) {
						return validationFailed([]validationError{{Resource: "Reference", Field: "sha", Code: "custom"}}, "Update is not a fast-forward")
					}
				}
				repo.Refs[refName] = newSHA
				repo.Touch()
				return RestResult{Status: 200, JSON: refObject(refName, newSHA)}
			}
			if method == "DELETE" {
				if _, ok := repo.Refs[refName]; !ok {
					return notFound()
				}
				delete(repo.Refs, refName)
				repo.Touch()
				return RestResult{Status: 204, JSON: nil}
			}
			return notFound()
		}
		return notFound()
	}
	if len(rest) >= 2 && rest[1] == "commits" && len(rest) == 3 && method == "GET" {
		sha := ResolveRef(repo, rest[2])
		if sha == "" {
			return notFound()
		}
		commitSHA := ResolveToCommit(repo, sha)
		if commitSHA == "" {
			return notFound()
		}
		commit := GetCommit(repo, commitSHA)
		if commit == nil {
			return notFound()
		}
		parents := make([]obj, 0, len(commit.Parents))
		for _, p := range commit.Parents {
			parents = append(parents, obj{
				"sha":      p,
				"url":      "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/commits/" + p,
				"html_url": "https://github.com/" + LOGIN + "/" + repo.Name + "/commit/" + p,
			})
		}
		return RestResult{Status: 200, JSON: obj{
			"sha":          commitSHA,
			"node_id":      GlobalID("Commit", commitSHA),
			"url":          "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/commits/" + commitSHA,
			"author":       parseIdent(ctx.Store, commit.Author),
			"committer":    parseIdent(ctx.Store, commit.Committer),
			"message":      commit.Message,
			"tree":         obj{"sha": commit.Tree, "url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/trees/" + commit.Tree},
			"parents":      parents,
			"verification": obj{"verified": false, "reason": "unsigned", "signature": nil, "payload": nil},
		}}
	}
	if len(rest) >= 2 && rest[1] == "trees" && len(rest) == 3 && method == "GET" {
		sha := ResolveRef(repo, rest[2])
		if sha == "" {
			return notFound()
		}
		commitSHA := ResolveToCommit(repo, sha)
		treeSHA := sha
		if commitSHA != "" {
			if commit := GetCommit(repo, commitSHA); commit != nil {
				treeSHA = commit.Tree
			}
		}
		recursive := false
		if v, ok := firstQuery(ctx.Query, "recursive"); ok && v == "1" {
			recursive = true
		}
		var entries []obj
		if recursive {
			flat := FlattenTree(repo, treeSHA)
			paths := make([]string, 0, len(flat))
			for p := range flat {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			entries = make([]obj, 0, len(paths))
			for _, p := range paths {
				e := flat[p]
				size := 0
				if o := repo.Objects[e.SHA]; o != nil {
					size = len(o.Data)
				}
				entries = append(entries, obj{
					"path": p,
					"mode": e.Mode,
					"type": "blob",
					"sha":  e.SHA,
					"size": size,
					"url":  "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/blobs/" + e.SHA,
				})
			}
		} else {
			list := GetTree(repo, treeSHA)
			if list == nil {
				return notFound()
			}
			entries = make([]obj, 0, len(list))
			for _, e := range list {
				entry := obj{
					"path": e.Name,
					"mode": e.Mode,
					"type": e.Kind,
					"sha":  e.SHA,
				}
				if e.Kind == "blob" {
					size := 0
					if o := repo.Objects[e.SHA]; o != nil {
						size = len(o.Data)
					}
					entry["size"] = size
					entry["url"] = "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/blobs/" + e.SHA
				} else {
					entry["url"] = "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/trees/" + e.SHA
				}
				entries = append(entries, entry)
			}
		}
		return RestResult{Status: 200, JSON: obj{
			"sha":       treeSHA,
			"url":       "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/trees/" + treeSHA,
			"tree":      entries,
			"truncated": false,
		}}
	}
	if len(rest) >= 2 && rest[1] == "blobs" && len(rest) == 3 && method == "GET" {
		o := repo.Objects[rest[2]]
		if o == nil || o.Type != ObjectBlob {
			return notFound()
		}
		return RestResult{Status: 200, JSON: obj{
			"sha":      rest[2],
			"node_id":  GlobalID("Blob", rest[2]),
			"size":     len(o.Data),
			"url":      "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/git/blobs/" + rest[2],
			"content":  base64.StdEncoding.EncodeToString(o.Data),
			"encoding": "base64",
		}}
	}
	return notFound()
}
