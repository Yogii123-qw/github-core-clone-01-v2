// GraphQL schema: type/field tables, introspection model, resolvers,
// selection execution, and the HTTP entry point.
package main

import (
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
)

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func base64Decode(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// --- Introspection model ---------------------------------------------------

// typeFieldOrder lists every object type in the order the reference
// schema declares them; __Schema.types follows this order.
var typeFieldOrder = []string{
	"Query", "Mutation", "Repository", "Issue", "PullRequest", "User", "Ref",
	"Commit", "Tree", "TreeEntry", "Blob", "Tag", "GitActor",
	"IssueConnection", "IssueEdge", "PullRequestConnection", "PullRequestEdge",
	"LabelConnection", "LabelEdge", "Label",
	"IssueCommentConnection", "IssueCommentEdge", "IssueComment",
	"PullRequestChangedFileConnection", "PullRequestChangedFileEdge", "PullRequestChangedFile",
	"CommitConnection", "CommitEdge",
	"SearchResultItemConnection", "SearchResultItemEdge", "PageInfo",
	"CreateIssuePayload", "CloseIssuePayload", "ReopenIssuePayload", "UpdateIssuePayload",
	"CreatePullRequestPayload", "MergePullRequestPayload", "AddCommentPayload", "RateLimit",
	"__Type", "__Field", "__InputValue", "__EnumValue", "__Schema", "__Directive",
}

// typeFields maps each object type to its field names (introspection).
var typeFields = map[string][]string{
	"Query":                            {"viewer", "repository", "node", "search", "user", "rateLimit"},
	"Mutation":                         {"createIssue", "closeIssue", "reopenIssue", "updateIssue", "createPullRequest", "mergePullRequest", "addComment"},
	"Repository":                       {"id", "databaseId", "name", "nameWithOwner", "owner", "description", "isPrivate", "url", "createdAt", "updatedAt", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequests", "pullRequest", "defaultBranchRef", "ref", "object", "stargazerCount", "forkCount", "isFork", "visibility", "homepageUrl", "primaryLanguage", "licenseInfo"},
	"Issue":                            {"id", "number", "title", "state", "body", "bodyText", "createdAt", "updatedAt", "closedAt", "author", "labels", "comments", "url", "repository", "stateReason", "closed", "viewerDidAuthor"},
	"PullRequest":                      {"id", "number", "title", "state", "body", "bodyText", "headRefName", "baseRefName", "headRef", "baseRef", "mergeable", "merged", "files", "createdAt", "updatedAt", "closedAt", "mergedAt", "author", "labels", "comments", "url", "repository", "isDraft", "mergeCommit", "additions", "deletions", "changedFiles", "commits", "viewerDidAuthor"},
	"User":                             {"id", "login", "name", "avatarUrl", "url", "email", "bio", "databaseId", "isViewer", "createdAt", "updatedAt"},
	"Ref":                              {"name", "prefix", "target", "repository"},
	"Commit":                           {"oid", "abbreviatedOid", "message", "messageHeadline", "committedDate", "authoredDate", "author", "committer", "parents", "tree", "additions", "deletions", "changedFiles", "url"},
	"Tree":                             {"oid", "abbreviatedOid", "entries"},
	"TreeEntry":                        {"name", "oid", "type", "mode", "object"},
	"Blob":                             {"oid", "abbreviatedOid", "text", "byteSize", "isBinary"},
	"Tag":                              {"oid", "abbreviatedOid", "name", "message", "target", "tagger"},
	"GitActor":                         {"name", "email", "date", "avatarUrl", "user"},
	"IssueConnection":                  {"nodes", "edges", "totalCount", "pageInfo"},
	"IssueEdge":                        {"node", "cursor"},
	"PullRequestConnection":            {"nodes", "edges", "totalCount", "pageInfo"},
	"PullRequestEdge":                  {"node", "cursor"},
	"LabelConnection":                  {"nodes", "edges", "totalCount", "pageInfo"},
	"LabelEdge":                        {"node", "cursor"},
	"Label":                            {"id", "name", "color", "description", "url"},
	"IssueCommentConnection":           {"nodes", "edges", "totalCount", "pageInfo"},
	"IssueCommentEdge":                 {"node", "cursor"},
	"IssueComment":                     {"id", "body", "bodyText", "author", "createdAt", "updatedAt", "url", "viewerDidAuthor"},
	"PullRequestChangedFileConnection": {"nodes", "edges", "totalCount", "pageInfo"},
	"PullRequestChangedFileEdge":       {"node", "cursor"},
	"PullRequestChangedFile":           {"path", "additions", "deletions", "changeType"},
	"CommitConnection":                 {"nodes", "edges", "totalCount", "pageInfo"},
	"CommitEdge":                       {"node", "cursor"},
	"SearchResultItemConnection":       {"nodes", "edges", "totalCount", "issueCount", "pageInfo", "codeCount", "repositoryCount", "userCount", "wikiCount", "discussionCount"},
	"SearchResultItemEdge":             {"node", "cursor", "textMatches"},
	"PageInfo":                         {"hasNextPage", "hasPreviousPage", "startCursor", "endCursor"},
	"CreateIssuePayload":               {"issue", "clientMutationId"},
	"CloseIssuePayload":                {"issue", "clientMutationId"},
	"ReopenIssuePayload":               {"issue", "clientMutationId"},
	"UpdateIssuePayload":               {"issue", "clientMutationId"},
	"CreatePullRequestPayload":         {"pullRequest", "clientMutationId"},
	"MergePullRequestPayload":          {"pullRequest", "clientMutationId"},
	"AddCommentPayload":                {"commentEdge", "subject", "clientMutationId"},
	"RateLimit":                        {"limit", "cost", "remaining", "resetAt"},
	"__Type":                           {"name", "kind", "description", "fields", "enumValues", "ofType", "inputFields", "interfaces", "possibleTypes"},
	"__Field":                          {"name", "description", "args", "type", "isDeprecated", "deprecationReason"},
	"__InputValue":                     {"name", "description", "type", "defaultValue"},
	"__EnumValue":                      {"name", "description", "isDeprecated", "deprecationReason"},
	"__Schema":                         {"queryType", "mutationType", "subscriptionType", "types", "directives"},
	"__Directive":                      {"name", "description", "locations", "args", "isRepeatable"},
}

var enumOrder = []string{
	"IssueState", "PullRequestState", "MergeableState", "SearchType",
	"IssueOrderField", "OrderDirection", "PullRequestOrderField", "ChangeType",
	"RepositoryPermission", "IssueStateReason", "RepositoryVisibility",
}

var enumValues = map[string][]string{
	"IssueState":            {"OPEN", "CLOSED"},
	"PullRequestState":      {"OPEN", "CLOSED", "MERGED"},
	"MergeableState":        {"MERGEABLE", "CONFLICTING", "UNKNOWN"},
	"SearchType":            {"ISSUE", "REPOSITORY", "USER", "DISCUSSION"},
	"IssueOrderField":       {"CREATED_AT", "UPDATED_AT", "COMMENTS"},
	"OrderDirection":        {"ASC", "DESC"},
	"PullRequestOrderField": {"CREATED_AT", "UPDATED_AT"},
	"ChangeType":            {"ADDED", "MODIFIED", "REMOVED", "RENAMED"},
	"RepositoryPermission":  {"ADMIN", "MAINTAIN", "WRITE", "TRIAGE", "READ", "NONE"},
	"IssueStateReason":      {"COMPLETED", "NOT_PLANNED", "DUPLICATE", "REOPENED"},
	"RepositoryVisibility":  {"PRIVATE", "PUBLIC", "INTERNAL"},
}

var scalarTypes = map[string]bool{
	"String": true, "Int": true, "Float": true, "Boolean": true, "ID": true,
	"URI": true, "DateTime": true, "GitObjectID": true, "HTML": true,
}

var interfaceTypes = map[string]bool{
	"Node": true, "GitObject": true, "SearchResultItem": true, "Actor": true,
	"UniformResourceLocatable": true, "Closable": true, "Updatable": true,
}

func introspectionType(name string) map[string]any {
	if fields, ok := typeFields[name]; ok {
		return map[string]any{
			"__typename": "__Type", "name": name, "kind": "OBJECT", "description": nil,
			"__fieldNames": fields, "__enumNames": nil,
		}
	}
	if values, ok := enumValues[name]; ok {
		return map[string]any{
			"__typename": "__Type", "name": name, "kind": "ENUM", "description": nil,
			"__fieldNames": nil, "__enumNames": values,
		}
	}
	if scalarTypes[name] {
		return map[string]any{
			"__typename": "__Type", "name": name, "kind": "SCALAR", "description": nil,
			"__fieldNames": nil, "__enumNames": nil,
		}
	}
	if interfaceTypes[name] {
		return map[string]any{
			"__typename": "__Type", "name": name, "kind": "INTERFACE", "description": nil,
			"__fieldNames": []string{}, "__enumNames": nil,
		}
	}
	return nil
}

// --- Schema resolvers ------------------------------------------------------

func labelsConnection(repo *Repo, names []string, args map[string]any) map[string]any {
	var items []map[string]any
	for _, n := range names {
		if l, ok := repo.Labels[n]; ok {
			items = append(items, map[string]any{
				"__typename": "Label", "__repo": repo,
				"name": l.Name, "color": l.Color, "description": l.Description,
				"id":  GlobalID("Label", strconv.Itoa(repo.DBID), strconv.Itoa(l.ID)),
				"url": "/api/v3/repos/" + LOGIN + "/" + repo.Name + "/labels/" + encodeURIComponent(l.Name),
			})
		}
	}
	window, pageInfo := paginateWindow(items, func(x map[string]any) string { return srcStr(x, "name") }, pageArgs(args))
	return connectionObject("LabelConnection", "LabelEdge", window, func(x map[string]any) string { return srcStr(x, "name") }, pageInfo, len(items))
}

func commentsConnection(repo *Repo, comments []*Comment, ownerNumber int, args map[string]any) map[string]any {
	items := make([]map[string]any, 0, len(comments))
	for _, c := range comments {
		items = append(items, commentObject(repo, c, ownerNumber))
	}
	keyOf := func(x map[string]any) string { return strconv.Itoa(srcComment(x).ID) }
	window, pageInfo := paginateWindow(items, keyOf, pageArgs(args))
	return connectionObject("IssueCommentConnection", "IssueCommentEdge", window, keyOf, pageInfo, len(items))
}

// numberArg coerces a GraphQL argument to an int the way JavaScript
// Number(value) does for numbers and numeric strings.
func numberArg(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return int(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func stringListArg(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func reposByID(store *Store) []*Repo {
	repos := make([]*Repo, 0, len(store.Repos))
	for _, r := range store.Repos {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].DBID < repos[j].DBID })
	return repos
}

func findRepoByDBID(store *Store, dbID int) *Repo {
	for _, r := range store.Repos {
		if r.DBID == dbID {
			return r
		}
	}
	return nil
}

func findIssueByGlobalID(store *Store, id string) (*Repo, *Issue) {
	kind, parts, ok := ParseGlobalID(id)
	if !ok || kind != "Issue" || len(parts) < 2 {
		return nil, nil
	}
	dbID, err1 := strconv.Atoi(parts[0])
	number, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	for _, repo := range store.Repos {
		if repo.DBID != dbID {
			continue
		}
		if issue, ok := repo.IssueByNumber[number]; ok {
			return repo, issue
		}
	}
	return nil, nil
}

// commitDate renders a git ident timestamp like the Commit date fields.
func commitDate(store *Store, line string) string {
	digits, ok := matchIdentDate(line)
	if !ok {
		return store.ISO(1)
	}
	sec, _ := strconv.ParseInt(digits, 10, 64)
	return isoSeconds(sec)
}

// matchIdentDate finds a trailing " <digits> <tz>" suffix, the way the
// reference pattern does: the digit run immediately before the final
// timezone. It returns the digits, or ok=false.
func matchIdentDate(line string) (string, bool) {
	if len(line) < 8 {
		return "", false
	}
	tz := line[len(line)-5:]
	if tz[0] != '+' && tz[0] != '-' {
		return "", false
	}
	for k := 1; k < 5; k++ {
		if tz[k] < '0' || tz[k] > '9' {
			return "", false
		}
	}
	if line[len(line)-6] != ' ' {
		return "", false
	}
	i := len(line) - 7
	for i >= 0 && line[i] >= '0' && line[i] <= '9' {
		i--
	}
	digits := line[i+1 : len(line)-6]
	if digits == "" {
		return "", false
	}
	if i < 0 || line[i] != ' ' {
		return "", false
	}
	return digits, true
}

// buildSchema constructs the resolver table.
func buildSchema() map[string]map[string]gqlResolver {
	schema := map[string]map[string]gqlResolver{
		"Query": {
			"viewer": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return userObject(LOGIN), nil
			},
			"repository": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				owner, _ := args["owner"].(string)
				name, _ := args["name"].(string)
				if owner != LOGIN {
					return nil, nil
				}
				repo := ctx.store.GetRepo(name)
				if repo == nil {
					return nil, nil
				}
				return repoObject(repo), nil
			},
			"node": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				kind, parts, ok := ParseGlobalID(gqlString(args["id"]))
				if !ok {
					return nil, nil
				}
				switch kind {
				case "Repository":
					if len(parts) < 1 {
						return nil, nil
					}
					dbID, err := strconv.Atoi(parts[0])
					if err != nil {
						return nil, nil
					}
					if repo := findRepoByDBID(ctx.store, dbID); repo != nil {
						return repoObject(repo), nil
					}
					return nil, nil
				case "Issue", "PullRequest":
					if len(parts) < 2 {
						return nil, nil
					}
					dbID, err1 := strconv.Atoi(parts[0])
					number, err2 := strconv.Atoi(parts[1])
					if err1 != nil || err2 != nil {
						return nil, nil
					}
					for _, repo := range ctx.store.Repos {
						if repo.DBID != dbID {
							continue
						}
						if kind == "Issue" {
							if issue, ok := repo.IssueByNumber[number]; ok {
								return issueObject(repo, issue), nil
							}
							return nil, nil
						}
						if pull, ok := repo.PullByNumber[number]; ok {
							return pullObject(repo, pull), nil
						}
						return nil, nil
					}
					return nil, nil
				case "User":
					return userObject(LOGIN), nil
				default:
					return nil, nil
				}
			},
			"search": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				query := gqlString(args["query"])
				typ := gqlString(args["type"])
				if _, ok := args["type"]; !ok {
					typ = "ISSUE"
				}
				var items []map[string]any
				if typ == "ISSUE" {
					var terms []string
					for _, t := range strings.Fields(strings.ToLower(query)) {
						if t != "" && !strings.HasPrefix(t, "repo:") && !strings.HasPrefix(t, "type:") {
							terms = append(terms, t)
						}
					}
					for _, repo := range reposByID(ctx.store) {
						for _, issue := range repo.Issues {
							body := ""
							if issue.Body != nil {
								body = *issue.Body
							}
							hay := strings.ToLower(issue.Title + " " + strings.ToLower(body))
							matched := len(terms) == 0
							if !matched {
								matched = true
								for _, t := range terms {
									if !strings.Contains(hay, t) {
										matched = false
										break
									}
								}
							}
							if matched {
								items = append(items, issueObject(repo, issue))
							}
						}
					}
				}
				keyOf := func(x map[string]any) string {
					return strconv.Itoa(srcRepo(x).DBID) + ":" + strconv.Itoa(srcIssue(x).Number)
				}
				window, pageInfo := paginateWindow(items, keyOf, pageArgs(args))
				conn := connectionObject("SearchResultItemConnection", "SearchResultItemEdge", window, keyOf, pageInfo, len(items))
				conn["issueCount"] = len(items)
				conn["codeCount"] = 0
				conn["repositoryCount"] = 0
				conn["userCount"] = 0
				conn["wikiCount"] = 0
				conn["discussionCount"] = 0
				return conn, nil
			},
			"user": func(_ map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				if login, ok := args["login"].(string); ok && login == LOGIN {
					return userObject(LOGIN), nil
				}
				return nil, nil
			},
			"rateLimit": func(_ map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return map[string]any{"__typename": "RateLimit", "limit": 5000, "cost": 1, "remaining": 4999, "resetAt": ctx.store.ISO(ctx.store.Seq + 3600)}, nil
			},
		},
		"Mutation": {
			"createIssue": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				input, _ := args["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				kind, parts, ok := ParseGlobalID(gqlString(input["repositoryId"]))
				if !ok || kind != "Repository" || len(parts) < 1 {
					return nil, fieldError("repository not found")
				}
				dbID, err := strconv.Atoi(parts[0])
				if err != nil {
					return nil, fieldError("repository not found")
				}
				repo := findRepoByDBID(ctx.store, dbID)
				if repo == nil {
					return nil, fieldError("repository not found")
				}
				var labelNames []string
				if ids, ok := input["labelIds"].([]any); ok {
					for _, rawID := range ids {
						lk, lp, lok := ParseGlobalID(gqlString(rawID))
						if lok && lk == "Label" && len(lp) >= 2 {
							if lid, err := strconv.Atoi(lp[1]); err == nil {
								for _, label := range repo.Labels {
									if label.ID == lid {
										labelNames = append(labelNames, label.Name)
									}
								}
							}
						}
					}
				}
				var bodyText *string
				if s, ok := input["body"].(string); ok {
					bodyText = &s
				}
				created, errRes := CreateIssueRecord(ctx.store, repo, gqlString(input["title"]), bodyText, labelNames)
				if errRes != nil {
					return nil, fieldError("could not create issue")
				}
				var clientID any
				if s, ok := input["clientMutationId"].(string); ok {
					clientID = s
				}
				return map[string]any{"__typename": "CreateIssuePayload", "issue": issueObject(repo, created), "clientMutationId": clientID}, nil
			},
			"closeIssue": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				input, _ := args["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				repo, issue := findIssueByGlobalID(ctx.store, gqlString(input["issueId"]))
				if issue == nil {
					return nil, fieldError("issue not found")
				}
				issue.State = StateClosed
				seq := ctx.store.NextSeq()
				issue.ClosedSeq = &seq
				issue.UpdatedSeq = seq
				repo.Touch()
				var clientID any
				if s, ok := input["clientMutationId"].(string); ok {
					clientID = s
				}
				return map[string]any{"__typename": "CloseIssuePayload", "issue": issueObject(repo, issue), "clientMutationId": clientID}, nil
			},
			"reopenIssue": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				input, _ := args["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				repo, issue := findIssueByGlobalID(ctx.store, gqlString(input["issueId"]))
				if issue == nil {
					return nil, fieldError("issue not found")
				}
				issue.State = StateOpen
				issue.ClosedSeq = nil
				issue.UpdatedSeq = ctx.store.NextSeq()
				repo.Touch()
				var clientID any
				if s, ok := input["clientMutationId"].(string); ok {
					clientID = s
				}
				return map[string]any{"__typename": "ReopenIssuePayload", "issue": issueObject(repo, issue), "clientMutationId": clientID}, nil
			},
			"updateIssue": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				input, _ := args["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				repo, issue := findIssueByGlobalID(ctx.store, gqlString(input["id"]))
				if issue == nil {
					return nil, fieldError("issue not found")
				}
				if s, ok := input["title"].(string); ok {
					issue.Title = s
				}
				if s, ok := input["body"].(string); ok {
					issue.Body = &s
				}
				if st, ok := input["state"].(string); ok {
					if st == "OPEN" {
						issue.State = StateOpen
						issue.ClosedSeq = nil
					} else if st == "CLOSED" {
						issue.State = StateClosed
						seq := ctx.store.NextSeq()
						issue.ClosedSeq = &seq
					}
				}
				issue.UpdatedSeq = ctx.store.NextSeq()
				repo.Touch()
				var clientID any
				if s, ok := input["clientMutationId"].(string); ok {
					clientID = s
				}
				return map[string]any{"__typename": "UpdateIssuePayload", "issue": issueObject(repo, issue), "clientMutationId": clientID}, nil
			},
			"createPullRequest": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				input, _ := args["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				kind, parts, ok := ParseGlobalID(gqlString(input["repositoryId"]))
				if !ok || kind != "Repository" || len(parts) < 1 {
					return nil, fieldError("repository not found")
				}
				dbID, err := strconv.Atoi(parts[0])
				if err != nil {
					return nil, fieldError("repository not found")
				}
				repo := findRepoByDBID(ctx.store, dbID)
				if repo == nil {
					return nil, fieldError("repository not found")
				}
				var bodyText *string
				if s, ok := input["body"].(string); ok {
					bodyText = &s
				}
				created, errRes := CreatePullRecord(ctx.store, repo, gqlString(input["title"]), bodyText, gqlString(input["headRefName"]), gqlString(input["baseRefName"]))
				if errRes != nil {
					return nil, fieldError("could not create pull request")
				}
				var clientID any
				if s, ok := input["clientMutationId"].(string); ok {
					clientID = s
				}
				return map[string]any{"__typename": "CreatePullRequestPayload", "pullRequest": pullObject(repo, created), "clientMutationId": clientID}, nil
			},
			"mergePullRequest": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				input, _ := args["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				kind, parts, ok := ParseGlobalID(gqlString(input["pullRequestId"]))
				if !ok || kind != "PullRequest" || len(parts) < 2 {
					return nil, fieldError("pull request not found")
				}
				dbID, err1 := strconv.Atoi(parts[0])
				number, err2 := strconv.Atoi(parts[1])
				if err1 != nil || err2 != nil {
					return nil, fieldError("pull request not found")
				}
				var foundRepo *Repo
				var foundPull *Pull
				for _, repo := range ctx.store.Repos {
					if repo.DBID != dbID {
						continue
					}
					if pull, ok := repo.PullByNumber[number]; ok {
						foundRepo, foundPull = repo, pull
					}
				}
				if foundPull == nil {
					return nil, fieldError("pull request not found")
				}
				var headline, bodyText *string
				if s, ok := input["commitHeadline"].(string); ok {
					headline = &s
				}
				if s, ok := input["commitBody"].(string); ok {
					bodyText = &s
				}
				_, mergeErr := MergePullRecord(ctx.store, foundRepo, foundPull, headline, bodyText)
				if mergeErr != "" {
					if mergeErr == MergeErrConflict {
						return nil, fieldError("merge conflict")
					}
					return nil, fieldError("pull request already merged")
				}
				var clientID any
				if s, ok := input["clientMutationId"].(string); ok {
					clientID = s
				}
				return map[string]any{"__typename": "MergePullRequestPayload", "pullRequest": pullObject(foundRepo, foundPull), "clientMutationId": clientID}, nil
			},
			"addComment": func(_ map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				input, _ := args["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				kind, parts, ok := ParseGlobalID(gqlString(input["subjectId"]))
				if !ok || len(parts) < 2 {
					return nil, fieldError("subject not found")
				}
				dbID, err1 := strconv.Atoi(parts[0])
				number, err2 := strconv.Atoi(parts[1])
				if err1 != nil || err2 != nil {
					return nil, fieldError("subject not found")
				}
				var subject map[string]any
				var repoFound *Repo
				var issueTarget *Issue
				var pullTarget *Pull
				for _, repo := range ctx.store.Repos {
					if repo.DBID != dbID {
						continue
					}
					if kind == "Issue" {
						if issue, ok := repo.IssueByNumber[number]; ok {
							subject, repoFound, issueTarget = issueObject(repo, issue), repo, issue
						}
					} else if kind == "PullRequest" {
						if pull, ok := repo.PullByNumber[number]; ok {
							subject, repoFound, pullTarget = pullObject(repo, pull), repo, pull
						}
					}
				}
				if subject == nil || repoFound == nil {
					return nil, fieldError("subject not found")
				}
				seq := ctx.store.NextSeq()
				comment := &Comment{ID: ctx.store.NextID(), Body: gqlString(input["body"]), User: LOGIN, CreatedSeq: seq, UpdatedSeq: seq}
				if issueTarget != nil {
					issueTarget.Comments = append(issueTarget.Comments, comment)
					issueTarget.UpdatedSeq = seq
				} else {
					pullTarget.Comments = append(pullTarget.Comments, comment)
					pullTarget.UpdatedSeq = seq
				}
				repoFound.Touch()
				node := commentObject(repoFound, comment, number)
				var clientID any
				if s, ok := input["clientMutationId"].(string); ok {
					clientID = s
				}
				return map[string]any{
					"__typename":       "AddCommentPayload",
					"commentEdge":      map[string]any{"__typename": "IssueCommentEdge", "node": node, "cursor": cursorFor(strconv.Itoa(comment.ID))},
					"subject":          subject,
					"clientMutationId": clientID,
				}, nil
			},
		},
		"Repository": {
			"id": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return GlobalID("Repository", strconv.Itoa(srcRepo(src).DBID)), nil
			},
			"databaseId": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcRepo(src).DBID, nil },
			"name":       func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcRepo(src).Name, nil },
			"nameWithOwner": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return LOGIN + "/" + srcRepo(src).Name, nil
			},
			"owner": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return userObject(LOGIN), nil },
			"description": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return srcRepo(src).Description, nil
			},
			"isPrivate": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcRepo(src).IsPrivate, nil },
			"url": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return "https://github.com/" + LOGIN + "/" + srcRepo(src).Name, nil
			},
			"createdAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcRepo(src).CreatedSeq), nil
			},
			"updatedAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcRepo(src).UpdatedSeq), nil
			},
			"viewerPermission": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return "ADMIN", nil },
			"hasIssuesEnabled": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return true, nil },
			"defaultBranchRef": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				sha, ok := repo.Refs["refs/heads/"+repo.DefaultBranch]
				if !ok {
					return nil, nil
				}
				return refObject(repo, repo.DefaultBranch, sha), nil
			},
			"issue": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				number, ok := numberArg(args["number"])
				if !ok {
					return nil, nil
				}
				issue, ok := repo.IssueByNumber[number]
				if !ok {
					return nil, nil
				}
				return issueObject(repo, issue), nil
			},
			"issues": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				items := append([]*Issue(nil), repo.Issues...)
				if states := stringListArg(args["states"]); len(states) > 0 {
					set := map[string]bool{}
					for _, s := range states {
						set[s] = true
					}
					filtered := items[:0]
					for _, i := range items {
						if set[issueState(i)] {
							filtered = append(filtered, i)
						}
					}
					items = filtered
				}
				if labels := stringListArg(args["labels"]); len(labels) > 0 {
					filtered := items[:0]
					for _, i := range items {
						all := true
						for _, l := range labels {
							found := false
							for _, mine := range i.Labels {
								if mine == l {
									found = true
									break
								}
							}
							if !found {
								all = false
								break
							}
						}
						if all {
							filtered = append(filtered, i)
						}
					}
					items = filtered
				}
				field, direction := parseOrderBy(args)
				items = sortIssues(items, field, direction)
				wrapped := make([]map[string]any, 0, len(items))
				for _, i := range items {
					wrapped = append(wrapped, issueObject(repo, i))
				}
				keyOf := func(x map[string]any) string { return strconv.Itoa(srcIssue(x).Number) }
				window, pageInfo := paginateWindow(wrapped, keyOf, pageArgs(args))
				return connectionObject("IssueConnection", "IssueEdge", window, keyOf, pageInfo, len(wrapped)), nil
			},
			"pullRequest": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				number, ok := numberArg(args["number"])
				if !ok {
					return nil, nil
				}
				pull, ok := repo.PullByNumber[number]
				if !ok {
					return nil, nil
				}
				return pullObject(repo, pull), nil
			},
			"pullRequests": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				items := append([]*Pull(nil), repo.Pulls...)
				if states := stringListArg(args["states"]); len(states) > 0 {
					set := map[string]bool{}
					for _, s := range states {
						set[s] = true
					}
					filtered := items[:0]
					for _, p := range items {
						if set[pullState(p)] {
							filtered = append(filtered, p)
						}
					}
					items = filtered
				}
				field, direction := parseOrderBy(args)
				items = sortPulls(items, field, direction)
				wrapped := make([]map[string]any, 0, len(items))
				for _, p := range items {
					wrapped = append(wrapped, pullObject(repo, p))
				}
				keyOf := func(x map[string]any) string { return strconv.Itoa(srcPull(x).Number) }
				window, pageInfo := paginateWindow(wrapped, keyOf, pageArgs(args))
				return connectionObject("PullRequestConnection", "PullRequestEdge", window, keyOf, pageInfo, len(wrapped)), nil
			},
			"ref": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				qualified := gqlString(args["qualifiedName"])
				sha, ok := repo.Refs[qualified]
				if !ok {
					sha, ok = repo.Refs["refs/heads/"+qualified]
				}
				if !ok {
					return nil, nil
				}
				name := qualified
				if strings.HasPrefix(qualified, "refs/heads/") {
					name = strings.TrimPrefix(qualified, "refs/heads/")
				}
				return refObject(repo, name, sha), nil
			},
			"object": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				expr := gqlString(args["expression"])
				if colon := strings.Index(expr, ":"); colon != -1 {
					refPart := expr[:colon]
					pathPart := expr[colon+1:]
					if refPart == "HEAD" {
						refPart = repo.DefaultBranch
					}
					sha := ResolveRef(repo, refPart)
					if sha == "" {
						return nil, nil
					}
					commitSHA := ResolveToCommit(repo, sha)
					if commitSHA == "" {
						return nil, nil
					}
					commit := GetCommit(repo, commitSHA)
					if commit == nil {
						return nil, nil
					}
					curTree := commit.Tree
					var parts []string
					for _, p := range strings.Split(pathPart, "/") {
						if p != "" {
							parts = append(parts, p)
						}
					}
					for i, part := range parts {
						entries := GetTree(repo, curTree)
						if entries == nil {
							return nil, nil
						}
						var entry *TreeEntry
						for j := range entries {
							if entries[j].Name == part {
								entry = &entries[j]
								break
							}
						}
						if entry == nil {
							return nil, nil
						}
						if i == len(parts)-1 {
							return gitObjectWrapper(repo, entry.SHA), nil
						}
						if entry.Kind != "tree" {
							return nil, nil
						}
						curTree = entry.SHA
					}
					return gitObjectWrapper(repo, curTree), nil
				}
				if expr == "HEAD" {
					expr = repo.DefaultBranch
				}
				sha := ResolveRef(repo, expr)
				if sha == "" {
					return nil, nil
				}
				return gitObjectWrapper(repo, sha), nil
			},
			"stargazerCount": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return 0, nil },
			"forkCount":      func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return 0, nil },
			"isFork":         func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return false, nil },
			"visibility": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return strings.ToUpper(visibilityOf(srcRepo(src).IsPrivate)), nil
			},
			"homepageUrl":     func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
			"primaryLanguage": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
			"licenseInfo":     func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
		},
		"Issue": {
			"id": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return GlobalID("Issue", strconv.Itoa(srcRepo(src).DBID), strconv.Itoa(srcIssue(src).Number)), nil
			},
			"number": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcIssue(src).Number, nil },
			"title":  func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcIssue(src).Title, nil },
			"state": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return issueState(srcIssue(src)), nil
			},
			"body": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if srcIssue(src).Body == nil {
					return "", nil
				}
				return *srcIssue(src).Body, nil
			},
			"bodyText": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if srcIssue(src).Body == nil {
					return "", nil
				}
				return *srcIssue(src).Body, nil
			},
			"createdAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcIssue(src).CreatedSeq), nil
			},
			"updatedAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcIssue(src).UpdatedSeq), nil
			},
			"closedAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if srcIssue(src).ClosedSeq == nil {
					return nil, nil
				}
				return ctx.store.ISO(*srcIssue(src).ClosedSeq), nil
			},
			"author": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return userObject(srcIssue(src).User), nil
			},
			"labels": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				return labelsConnection(srcRepo(src), srcIssue(src).Labels, args), nil
			},
			"comments": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				return commentsConnection(srcRepo(src), srcIssue(src).Comments, srcIssue(src).Number, args), nil
			},
			"url": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return "https://github.com/" + LOGIN + "/" + srcRepo(src).Name + "/issues/" + strconv.Itoa(srcIssue(src).Number), nil
			},
			"repository": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return repoObject(srcRepo(src)), nil
			},
			"stateReason": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if srcIssue(src).State == StateClosed {
					return "COMPLETED", nil
				}
				return nil, nil
			},
			"closed": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return srcIssue(src).State == StateClosed, nil
			},
			"viewerDidAuthor": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return true, nil },
		},
		"PullRequest": {
			"id": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return GlobalID("PullRequest", strconv.Itoa(srcRepo(src).DBID), strconv.Itoa(srcPull(src).Number)), nil
			},
			"number": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcPull(src).Number, nil },
			"title":  func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcPull(src).Title, nil },
			"state": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return pullState(srcPull(src)), nil
			},
			"body": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if srcPull(src).Body == nil {
					return "", nil
				}
				return *srcPull(src).Body, nil
			},
			"bodyText": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if srcPull(src).Body == nil {
					return "", nil
				}
				return *srcPull(src).Body, nil
			},
			"headRefName": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcPull(src).HeadRef, nil },
			"baseRefName": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcPull(src).BaseRef, nil },
			"headRef": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				pull := srcPull(src)
				sha := ResolveRef(srcRepo(src), pull.HeadRef)
				if sha == "" {
					sha = pull.HeadSHA
				}
				return refObject(srcRepo(src), pull.HeadRef, sha), nil
			},
			"baseRef": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				pull := srcPull(src)
				sha := ResolveRef(srcRepo(src), pull.BaseRef)
				if sha == "" {
					sha = pull.BaseSHA
				}
				return refObject(srcRepo(src), pull.BaseRef, sha), nil
			},
			"mergeable": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				pull := srcPull(src)
				if pull.State != StateOpen {
					return "UNKNOWN", nil
				}
				if PRMergeInfo(ctx.store, srcRepo(src), pull).Conflict {
					return "CONFLICTING", nil
				}
				return "MERGEABLE", nil
			},
			"merged": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcPull(src).Merged, nil },
			"files": func(src map[string]any, args map[string]any, ctx *gqlCtx) (any, error) {
				pull := srcPull(src)
				info := PRMergeInfo(ctx.store, srcRepo(src), pull)
				items := make([]map[string]any, 0, len(info.Files))
				for _, f := range info.Files {
					changeType := "MODIFIED"
					if f.Status == "added" {
						changeType = "ADDED"
					} else if f.Status == "removed" {
						changeType = "REMOVED"
					}
					items = append(items, map[string]any{
						"__typename": "PullRequestChangedFile",
						"path":       f.Filename, "additions": f.Additions, "deletions": f.Deletions, "changeType": changeType,
					})
				}
				keyOf := func(x map[string]any) string { return srcStr(x, "path") }
				window, pageInfo := paginateWindow(items, keyOf, pageArgs(args))
				return connectionObject("PullRequestChangedFileConnection", "PullRequestChangedFileEdge", window, keyOf, pageInfo, len(items)), nil
			},
			"createdAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcPull(src).CreatedSeq), nil
			},
			"updatedAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcPull(src).UpdatedSeq), nil
			},
			"closedAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if srcPull(src).ClosedSeq == nil {
					return nil, nil
				}
				return ctx.store.ISO(*srcPull(src).ClosedSeq), nil
			},
			"mergedAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if srcPull(src).MergedSeq == nil {
					return nil, nil
				}
				return ctx.store.ISO(*srcPull(src).MergedSeq), nil
			},
			"author": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return userObject(srcPull(src).User), nil
			},
			"labels": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				return labelsConnection(srcRepo(src), []string{}, args), nil
			},
			"comments": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				return commentsConnection(srcRepo(src), srcPull(src).Comments, srcPull(src).Number, args), nil
			},
			"url": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return "https://github.com/" + LOGIN + "/" + srcRepo(src).Name + "/pull/" + strconv.Itoa(srcPull(src).Number), nil
			},
			"repository": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return repoObject(srcRepo(src)), nil
			},
			"isDraft": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return false, nil },
			"mergeCommit": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				pull := srcPull(src)
				if pull.MergeSHA == nil {
					return nil, nil
				}
				return commitObject(srcRepo(src), *pull.MergeSHA), nil
			},
			"additions": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return PRMergeInfo(ctx.store, srcRepo(src), srcPull(src)).Additions, nil
			},
			"deletions": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return PRMergeInfo(ctx.store, srcRepo(src), srcPull(src)).Deletions, nil
			},
			"changedFiles": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return PRMergeInfo(ctx.store, srcRepo(src), srcPull(src)).ChangedFiles, nil
			},
			"commits": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				pull := srcPull(src)
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
				items := make([]map[string]any, 0, len(order))
				for _, sha := range order {
					items = append(items, commitObject(repo, sha))
				}
				keyOf := func(x map[string]any) string { return srcSHA(x) }
				window, pageInfo := paginateWindow(items, keyOf, pageArgs(args))
				return connectionObject("CommitConnection", "CommitEdge", window, keyOf, pageInfo, len(items)), nil
			},
			"viewerDidAuthor": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return true, nil },
		},
		"User": {
			"id":    func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return GlobalID("User", "1"), nil },
			"login": prop("login"),
			"name":  prop("login"),
			"avatarUrl": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return "https://avatars.githubusercontent.com/u/1?v=4", nil
			},
			"url": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return "https://github.com/" + srcStr(src, "login"), nil
			},
			"email":      func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
			"bio":        func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
			"databaseId": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return 1, nil },
			"isViewer":   func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return true, nil },
			"createdAt":  func(_ map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) { return ctx.store.ISO(1), nil },
			"updatedAt":  func(_ map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) { return ctx.store.ISO(1), nil },
		},
		"Ref": {
			"name":   prop("name"),
			"prefix": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return "refs/heads/", nil },
			"target": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return gitObjectWrapper(srcRepo(src), srcSHA(src)), nil
			},
			"repository": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return repoObject(srcRepo(src)), nil
			},
		},
		"Commit": {
			"oid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcSHA(src), nil },
			"abbreviatedOid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				sha := srcSHA(src)
				if len(sha) > 7 {
					return sha[:7], nil
				}
				return sha, nil
			},
			"message": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if c := GetCommit(srcRepo(src), srcSHA(src)); c != nil {
					return c.Message, nil
				}
				return "", nil
			},
			"messageHeadline": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				msg := ""
				if c := GetCommit(srcRepo(src), srcSHA(src)); c != nil {
					msg = c.Message
				}
				return strings.SplitN(msg, "\n", 2)[0], nil
			},
			"committedDate": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if c := GetCommit(srcRepo(src), srcSHA(src)); c != nil {
					return commitDate(ctx.store, c.Committer), nil
				}
				return ctx.store.ISO(1), nil
			},
			"authoredDate": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if c := GetCommit(srcRepo(src), srcSHA(src)); c != nil {
					return commitDate(ctx.store, c.Author), nil
				}
				return ctx.store.ISO(1), nil
			},
			"author": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if c := GetCommit(srcRepo(src), srcSHA(src)); c != nil {
					return gitActorObject(c.Author, ctx.store), nil
				}
				return nil, nil
			},
			"committer": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if c := GetCommit(srcRepo(src), srcSHA(src)); c != nil {
					return gitActorObject(c.Committer, ctx.store), nil
				}
				return nil, nil
			},
			"parents": func(src map[string]any, args map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				var items []map[string]any
				if c := GetCommit(repo, srcSHA(src)); c != nil {
					for _, p := range c.Parents {
						items = append(items, commitObject(repo, p))
					}
				}
				keyOf := func(x map[string]any) string { return srcSHA(x) }
				window, pageInfo := paginateWindow(items, keyOf, pageArgs(args))
				return connectionObject("CommitConnection", "CommitEdge", window, keyOf, pageInfo, len(items)), nil
			},
			"tree": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if c := GetCommit(srcRepo(src), srcSHA(src)); c != nil {
					return map[string]any{"__typename": "Tree", "__repo": srcRepo(src), "__sha": c.Tree}, nil
				}
				return nil, nil
			},
			"additions": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				c := GetCommit(repo, srcSHA(src))
				if c == nil {
					return 0, nil
				}
				var parentTree string
				if len(c.Parents) > 0 {
					if pc := GetCommit(repo, c.Parents[0]); pc != nil {
						parentTree = pc.Tree
					}
				}
				total := 0
				for _, f := range DiffTrees(repo, parentTree, c.Tree) {
					total += f.Additions
				}
				return total, nil
			},
			"deletions": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				c := GetCommit(repo, srcSHA(src))
				if c == nil {
					return 0, nil
				}
				var parentTree string
				if len(c.Parents) > 0 {
					if pc := GetCommit(repo, c.Parents[0]); pc != nil {
						parentTree = pc.Tree
					}
				}
				total := 0
				for _, f := range DiffTrees(repo, parentTree, c.Tree) {
					total += f.Deletions
				}
				return total, nil
			},
			"changedFiles": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				repo := srcRepo(src)
				c := GetCommit(repo, srcSHA(src))
				if c == nil {
					return 0, nil
				}
				var parentTree string
				if len(c.Parents) > 0 {
					if pc := GetCommit(repo, c.Parents[0]); pc != nil {
						parentTree = pc.Tree
					}
				}
				return len(DiffTrees(repo, parentTree, c.Tree)), nil
			},
			"url": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return "https://github.com/" + LOGIN + "/" + srcRepo(src).Name + "/commit/" + srcSHA(src), nil
			},
		},
		"Tree": {
			"oid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcSHA(src), nil },
			"abbreviatedOid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				sha := srcSHA(src)
				if len(sha) > 7 {
					return sha[:7], nil
				}
				return sha, nil
			},
			"entries": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				entries := GetTree(srcRepo(src), srcSHA(src))
				out := make([]any, 0, len(entries))
				for _, e := range entries {
					out = append(out, map[string]any{
						"__typename": "TreeEntry", "__repo": srcRepo(src),
						"name": e.Name, "oid": e.SHA, "type": e.Kind, "mode": e.Mode, "__entrySHA": e.SHA,
					})
				}
				return out, nil
			},
		},
		"TreeEntry": {
			"name": prop("name"),
			"oid":  prop("oid"),
			"type": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				t := srcStr(src, "type")
				if t == "tree" || t == "commit" {
					return t, nil
				}
				return "blob", nil
			},
			"mode": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				n, err := strconv.ParseInt(srcStr(src, "mode"), 8, 64)
				if err != nil {
					return nil, nil
				}
				return int(n), nil
			},
			"object": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				sha, _ := src["__entrySHA"].(string)
				return gitObjectWrapper(srcRepo(src), sha), nil
			},
		},
		"Blob": {
			"oid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcSHA(src), nil },
			"abbreviatedOid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				sha := srcSHA(src)
				if len(sha) > 7 {
					return sha[:7], nil
				}
				return sha, nil
			},
			"text": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if o := srcRepo(src).Objects[srcSHA(src)]; o != nil {
					return string(o.Data), nil
				}
				return "", nil
			},
			"byteSize": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if o := srcRepo(src).Objects[srcSHA(src)]; o != nil {
					return len(o.Data), nil
				}
				return 0, nil
			},
			"isBinary": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if o := srcRepo(src).Objects[srcSHA(src)]; o != nil {
					return strings.IndexByte(string(o.Data), 0) != -1, nil
				}
				return false, nil
			},
		},
		"Tag": {
			"oid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcSHA(src), nil },
			"abbreviatedOid": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				sha := srcSHA(src)
				if len(sha) > 7 {
					return sha[:7], nil
				}
				return sha, nil
			},
			"name": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if t := GetTag(srcRepo(src), srcSHA(src)); t != nil {
					return t.Tag, nil
				}
				return "", nil
			},
			"message": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if t := GetTag(srcRepo(src), srcSHA(src)); t != nil {
					return t.Message, nil
				}
				return "", nil
			},
			"target": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				if t := GetTag(srcRepo(src), srcSHA(src)); t != nil {
					return gitObjectWrapper(srcRepo(src), t.Object), nil
				}
				return nil, nil
			},
			"tagger": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				if t := GetTag(srcRepo(src), srcSHA(src)); t != nil {
					return gitActorObject(t.Tagger, ctx.store), nil
				}
				return nil, nil
			},
		},
		"GitActor": {
			"name":  prop("name"),
			"email": prop("email"),
			"date":  prop("date"),
			"avatarUrl": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return "https://avatars.githubusercontent.com/u/1?v=4", nil
			},
			"user": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
		},
		"Label": {
			"id":          prop("id"),
			"name":        prop("name"),
			"color":       prop("color"),
			"description": prop("description"),
			"url":         prop("url"),
		},
		"IssueComment": {
			"id": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return GlobalID("IssueComment", strconv.Itoa(srcRepo(src).DBID), strconv.Itoa(srcComment(src).ID)), nil
			},
			"body":     func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcComment(src).Body, nil },
			"bodyText": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return srcComment(src).Body, nil },
			"author": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return userObject(srcComment(src).User), nil
			},
			"createdAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcComment(src).CreatedSeq), nil
			},
			"updatedAt": func(src map[string]any, _ map[string]any, ctx *gqlCtx) (any, error) {
				return ctx.store.ISO(srcComment(src).UpdatedSeq), nil
			},
			"url": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				owner, _ := src["__ownerNumber"].(int)
				return "https://github.com/" + LOGIN + "/" + srcRepo(src).Name + "/issues/" + strconv.Itoa(owner) + "#issuecomment-" + strconv.Itoa(srcComment(src).ID), nil
			},
			"viewerDidAuthor": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return true, nil },
		},
		"PullRequestChangedFile": {
			"path":       prop("path"),
			"additions":  prop("additions"),
			"deletions":  prop("deletions"),
			"changeType": prop("changeType"),
		},
		"PageInfo": {
			"hasNextPage":     prop("hasNextPage"),
			"hasPreviousPage": prop("hasPreviousPage"),
			"startCursor":     prop("startCursor"),
			"endCursor":       prop("endCursor"),
		},
		"RateLimit": {
			"limit":     prop("limit"),
			"cost":      prop("cost"),
			"remaining": prop("remaining"),
			"resetAt":   prop("resetAt"),
		},
		"__Type": {
			"name":        prop("name"),
			"kind":        prop("kind"),
			"description": prop("description"),
			"fields": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				names, _ := src["__fieldNames"].([]string)
				out := make([]any, 0, len(names))
				for _, name := range names {
					out = append(out, map[string]any{
						"__typename": "__Field", "name": name, "description": nil,
						"args": []any{}, "type": introspectionType("String"),
						"isDeprecated": false, "deprecationReason": nil,
					})
				}
				return out, nil
			},
			"enumValues": func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				names, _ := src["__enumNames"].([]string)
				out := make([]any, 0, len(names))
				for _, name := range names {
					out = append(out, map[string]any{
						"__typename": "__EnumValue", "name": name, "description": nil,
						"isDeprecated": false, "deprecationReason": nil,
					})
				}
				return out, nil
			},
			"ofType":        func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
			"inputFields":   func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return []any{}, nil },
			"interfaces":    func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return []any{}, nil },
			"possibleTypes": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return []any{}, nil },
		},
		"__Field": {
			"name":              prop("name"),
			"description":       prop("description"),
			"args":              prop("args"),
			"type":              prop("type"),
			"isDeprecated":      prop("isDeprecated"),
			"deprecationReason": prop("deprecationReason"),
		},
		"__InputValue": {
			"name":         prop("name"),
			"description":  prop("description"),
			"type":         prop("type"),
			"defaultValue": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
		},
		"__EnumValue": {
			"name":              prop("name"),
			"description":       prop("description"),
			"isDeprecated":      prop("isDeprecated"),
			"deprecationReason": prop("deprecationReason"),
		},
		"__Schema": {
			"queryType": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return introspectionType("Query"), nil
			},
			"mutationType": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				return introspectionType("Mutation"), nil
			},
			"subscriptionType": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
			"types": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
				out := make([]any, 0, len(typeFieldOrder)+len(enumOrder))
				for _, n := range typeFieldOrder {
					if t := introspectionType(n); t != nil {
						out = append(out, t)
					}
				}
				for _, n := range enumOrder {
					if t := introspectionType(n); t != nil {
						out = append(out, t)
					}
				}
				return out, nil
			},
			"directives": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return []any{}, nil },
		},
		"__Directive": {
			"name":         prop("name"),
			"description":  prop("description"),
			"locations":    func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return []any{}, nil },
			"args":         func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return []any{}, nil },
			"isRepeatable": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return false, nil },
		},
	}
	for _, conn := range []string{"IssueConnection", "PullRequestConnection", "LabelConnection", "IssueCommentConnection", "PullRequestChangedFileConnection", "CommitConnection", "SearchResultItemConnection"} {
		schema[conn] = map[string]gqlResolver{
			"nodes":           prop("nodes"),
			"edges":           prop("edges"),
			"totalCount":      prop("totalCount"),
			"pageInfo":        prop("pageInfo"),
			"issueCount":      prop("issueCount"),
			"codeCount":       prop("codeCount"),
			"repositoryCount": prop("repositoryCount"),
			"userCount":       prop("userCount"),
			"wikiCount":       prop("wikiCount"),
			"discussionCount": prop("discussionCount"),
		}
	}
	for _, edge := range []string{"IssueEdge", "PullRequestEdge", "LabelEdge", "IssueCommentEdge", "PullRequestChangedFileEdge", "CommitEdge", "SearchResultItemEdge"} {
		schema[edge] = map[string]gqlResolver{
			"node":        prop("node"),
			"cursor":      prop("cursor"),
			"textMatches": func(_ map[string]any, _ map[string]any, _ *gqlCtx) (any, error) { return nil, nil },
		}
	}
	for _, payload := range []string{"CreateIssuePayload", "CloseIssuePayload", "ReopenIssuePayload", "UpdateIssuePayload"} {
		schema[payload] = map[string]gqlResolver{"issue": prop("issue"), "clientMutationId": prop("clientMutationId")}
	}
	for _, payload := range []string{"CreatePullRequestPayload", "MergePullRequestPayload"} {
		schema[payload] = map[string]gqlResolver{"pullRequest": prop("pullRequest"), "clientMutationId": prop("clientMutationId")}
	}
	schema["AddCommentPayload"] = map[string]gqlResolver{
		"commentEdge": prop("commentEdge"), "subject": prop("subject"), "clientMutationId": prop("clientMutationId"),
	}
	return schema
}

var gqlSchema = buildSchema()

// --- Selection execution ---------------------------------------------------

func directivesAllow(directives []gqlDirective, variables map[string]any) bool {
	for _, d := range directives {
		if d.name != "skip" && d.name != "include" {
			continue
		}
		var value any = false
		for _, a := range d.args {
			if a.name == "if" {
				value = valueFromNode(a.value, variables)
				break
			}
		}
		if d.name == "skip" && value == true {
			return false
		}
		if d.name == "include" && value != true {
			return false
		}
	}
	return true
}

func fragmentApplies(typeCond, typeName string) bool {
	if typeCond == typeName {
		return true
	}
	switch typeCond {
	case "Node", "GitObject", "Actor":
		return true
	default:
		return false
	}
}

// stripInternal returns a copy of a wrapper map without internal
// ("__"-prefixed, except "__typename") keys, for the rare case a composite
// value is returned without a sub-selection.
func stripInternal(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		if strings.HasPrefix(k, "__") && k != "__typename" {
			continue
		}
		out[k] = v
	}
	return out
}

func executeSelection(selections []*gqlSelection, src map[string]any, typeName string, ctx *gqlCtx, path []any) (map[string]any, error) {
	out := make(map[string]any)
	def := gqlSchema[typeName]
	for _, sel := range selections {
		switch sel.kind {
		case "fragmentSpread":
			if !directivesAllow(sel.directives, ctx.variables) {
				continue
			}
			frag, ok := ctx.fragments[sel.fragName]
			if !ok {
				continue
			}
			if !fragmentApplies(frag.typeCond, typeName) {
				continue
			}
			if !directivesAllow(frag.directives, ctx.variables) {
				continue
			}
			merged, err := executeSelection(frag.selection, src, typeName, ctx, path)
			if err != nil {
				return nil, err
			}
			for k, v := range merged {
				out[k] = v
			}
			continue
		case "inlineFragment":
			if !directivesAllow(sel.directives, ctx.variables) {
				continue
			}
			if sel.typeCond != nil && !fragmentApplies(*sel.typeCond, typeName) {
				continue
			}
			merged, err := executeSelection(sel.selection, src, typeName, ctx, path)
			if err != nil {
				return nil, err
			}
			for k, v := range merged {
				out[k] = v
			}
			continue
		}
		field := sel.field
		if !directivesAllow(field.directives, ctx.variables) {
			continue
		}
		responseKey := field.name
		if field.alias != nil {
			responseKey = *field.alias
		}
		if field.name == "__typename" {
			out[responseKey] = typeName
			continue
		}
		if field.name == "__type" || field.name == "__schema" {
			var value any
			childType := "__Schema"
			if field.name == "__type" {
				args := argsObject(field, ctx.variables)
				value = introspectionType(gqlString(args["name"]))
				childType = "__Type"
			} else {
				value = map[string]any{"__typename": "__Schema"}
			}
			completed, err := completeValue(value, childType, true, field, ctx, append(path, responseKey))
			if err != nil {
				return nil, err
			}
			out[responseKey] = completed
			continue
		}
		resolver, ok := def[field.name]
		if !ok {
			return nil, &unknownFieldError{field: field.name, typeName: typeName}
		}
		args := argsObject(field, ctx.variables)
		value, err := resolver(src, args, ctx)
		if err != nil {
			var unknown *unknownFieldError
			if errors.As(err, &unknown) {
				return nil, err
			}
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: err.Error(), path: append(append([]any{}, path...), responseKey), hasPath: true})
			out[responseKey] = nil
			continue
		}
		completed, err := completeValue(value, "", false, field, ctx, append(path, responseKey))
		if err != nil {
			return nil, err
		}
		out[responseKey] = completed
	}
	return out, nil
}

func completeValue(value any, forcedType string, hasForced bool, field *gqlField, ctx *gqlCtx, path []any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if arr, ok := value.([]any); ok {
		out := make([]any, 0, len(arr))
		for i, item := range arr {
			completed, err := completeValue(item, forcedType, hasForced, field, ctx, append(path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, completed)
		}
		return out, nil
	}
	if m, ok := value.(map[string]any); ok {
		typeName := forcedType
		if !hasForced {
			typeName, _ = m["__typename"].(string)
		}
		if typeName == "" {
			return m, nil
		}
		if field == nil || !field.hasSel || len(field.selection) == 0 {
			return stripInternal(m), nil
		}
		return executeSelection(field.selection, m, typeName, ctx, path)
	}
	return value, nil
}

func executeOperation(store *Store, doc *gqlDocument, operation *gqlOperation, variables map[string]any) (map[string]any, error) {
	vars := make(map[string]any, len(variables))
	for k, v := range variables {
		vars[k] = v
	}
	for _, def := range operation.varDefs {
		if _, ok := vars[def.name]; !ok && def.defaultValue != nil {
			vars[def.name] = valueFromNode(def.defaultValue, vars)
		}
	}
	ctx := &gqlCtx{store: store, variables: vars, fragments: doc.fragments}
	rootType := "Query"
	if operation.kind == "mutation" {
		rootType = "Mutation"
	}
	root := map[string]any{"__typename": rootType}
	data, err := executeSelection(operation.selection, root, rootType, ctx, []any{})
	if err != nil {
		var unknown *unknownFieldError
		if errors.As(err, &unknown) {
			return map[string]any{"data": nil, "errors": []any{map[string]any{"message": unknown.Error()}}}, nil
		}
		return nil, err
	}
	result := map[string]any{"data": data}
	if len(ctx.errors) > 0 {
		errList := make([]any, 0, len(ctx.errors))
		for _, e := range ctx.errors {
			entry := map[string]any{"message": e.message}
			if e.hasPath {
				entry["path"] = e.path
			}
			errList = append(errList, entry)
		}
		result["errors"] = errList
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// HTTP entry point
// ---------------------------------------------------------------------------

// GraphQLResult is one GraphQL HTTP answer.
type GraphQLResult struct {
	Status int
	JSON   any
}

// HandleGraphQL executes one request body (a single operation or a batch
// array). It always answers HTTP 200 for well-formed envelopes.
func HandleGraphQL(store *Store, body any) (GraphQLResult, error) {
	runOne := func(item any) (map[string]any, error) {
		if item == nil {
			return map[string]any{"data": nil, "errors": []any{map[string]any{"message": "request body must be an object with a query string"}}}, nil
		}
		m, ok := item.(map[string]any)
		if !ok {
			return map[string]any{"data": nil, "errors": []any{map[string]any{"message": "request body must be an object with a query string"}}}, nil
		}
		query, isStr := m["query"].(string)
		if !isStr || strings.TrimSpace(query) == "" {
			return map[string]any{"data": nil, "errors": []any{map[string]any{"message": "must provide a query string"}}}, nil
		}
		vars := map[string]any{}
		if v, ok := m["variables"].(map[string]any); ok {
			vars = v
		}
		doc, err := parseGraphQL(query)
		if err != nil {
			return map[string]any{"data": nil, "errors": []any{map[string]any{"message": "syntax error: " + err.Error()}}}, nil
		}
		var operation *gqlOperation
		if len(doc.operations) > 0 {
			operation = doc.operations[0]
		}
		if name, ok := m["operationName"].(string); ok {
			for _, op := range doc.operations {
				if op.name != nil && *op.name == name {
					operation = op
					break
				}
			}
		}
		if operation == nil {
			return map[string]any{"data": nil, "errors": []any{map[string]any{"message": "no operation found"}}}, nil
		}
		return executeOperation(store, doc, operation, vars)
	}
	if arr, ok := body.([]any); ok {
		out := make([]any, 0, len(arr))
		for _, item := range arr {
			res, err := runOne(item)
			if err != nil {
				return GraphQLResult{}, err
			}
			out = append(out, res)
		}
		return GraphQLResult{Status: 200, JSON: out}, nil
	}
	res, err := runOne(body)
	if err != nil {
		return GraphQLResult{}, err
	}
	return GraphQLResult{Status: 200, JSON: res}, nil
}
