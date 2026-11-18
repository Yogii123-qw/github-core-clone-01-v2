// Model Context Protocol endpoint: JSON-RPC 2.0 over Streamable HTTP,
// answering application/json. Speaks revision 2026-07-28
// (server/discover) and the 2025-11-25 handshake (initialize +
// notifications/initialized). Six tools, all acting on the same store as
// REST, with stable, byte-identical tools/list answers.
package main

import (
	"math"
	"strconv"
	"strings"
)

// MCPVersions lists the protocol revisions this server negotiates, newest
// first.
var MCPVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type toolDef struct {
	name        string
	description string
	schema      map[string]any
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

var mcpTools = []toolDef{
	{
		name:        "create_repo",
		description: "Create a new repository owned by the authenticated user.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        stringProp("The name of the repository."),
				"description": stringProp("A short description of the repository."),
				"private":     map[string]any{"type": "boolean", "description": "Whether the repository is private."},
			},
			"required":             []any{"name"},
			"additionalProperties": true,
		},
	},
	{
		name:        "get_repo",
		description: "Get a repository by owner and name.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"owner":     stringProp("The repository owner login."),
				"repo":      stringProp("The repository name."),
				"full_name": stringProp("The full repository name as owner/repo."),
			},
			"required":             []any{"owner", "repo"},
			"additionalProperties": true,
		},
	},
	{
		name:        "create_issue",
		description: "Create an issue in a repository.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"owner":     stringProp("The repository owner login."),
				"repo":      stringProp("The repository name."),
				"full_name": stringProp("The full repository name as owner/repo."),
				"title":     stringProp("The issue title."),
				"body":      stringProp("The issue body."),
				"labels":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Label names; each must already exist."},
			},
			"required":             []any{"owner", "repo", "title"},
			"additionalProperties": true,
		},
	},
	{
		name:        "list_issues",
		description: "List issues in a repository, optionally filtered by state.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"owner":     stringProp("The repository owner login."),
				"repo":      stringProp("The repository name."),
				"full_name": stringProp("The full repository name as owner/repo."),
				"state":     map[string]any{"type": "string", "description": "Issue state filter: open, closed, or all.", "enum": []any{"open", "closed", "all"}},
			},
			"required":             []any{"owner", "repo"},
			"additionalProperties": true,
		},
	},
	{
		name:        "create_pull_request",
		description: "Create a pull request in a repository.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"owner":     stringProp("The repository owner login."),
				"repo":      stringProp("The repository name."),
				"full_name": stringProp("The full repository name as owner/repo."),
				"title":     stringProp("The pull request title."),
				"body":      stringProp("The pull request body."),
				"head":      stringProp("The head branch name."),
				"base":      stringProp("The base branch name."),
			},
			"required":             []any{"owner", "repo", "title", "head", "base"},
			"additionalProperties": true,
		},
	},
	{
		name:        "merge_pull_request",
		description: "Merge a pull request as a squash commit on its base branch.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"owner":       stringProp("The repository owner login."),
				"repo":        stringProp("The repository name."),
				"full_name":   stringProp("The full repository name as owner/repo."),
				"pull_number": map[string]any{"type": "integer", "description": "The pull request number."},
			},
			"required":             []any{"owner", "repo", "pull_number"},
			"additionalProperties": true,
		},
	},
}

// toolsListJSON is built once so two tools/list calls answer byte for
// byte alike.
var toolsListJSON = mustMarshalJSON(map[string]any{
	"tools": func() []any {
		out := make([]any, 0, len(mcpTools))
		for _, t := range mcpTools {
			out = append(out, map[string]any{
				"name":        t.name,
				"description": t.description,
				"inputSchema": t.schema,
			})
		}
		return out
	}(),
})

func mcpResult(id any, payload any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": payload}
}

func mcpRPCError(id any, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

// rpcFailure is the internal signal that a tool call must answer with a
// JSON-RPC error instead of a tool result.
type rpcFailure struct {
	code    int
	message string
}

func toolText(payload any, isError bool) map[string]any {
	var text string
	if s, ok := payload.(string); ok {
		text = s
	} else {
		text = string(mustMarshalJSON(payload))
	}
	out := map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": text}},
		"structuredContent": payload,
	}
	if isError {
		out["isError"] = true
	}
	return out
}

func missingArg(name string) map[string]any {
	return toolText(map[string]any{"error": "missing required argument: " + name}, true)
}

func pickString(args map[string]any, names []string) (string, bool) {
	for _, name := range names {
		if v, ok := args[name].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

type ownerRepo struct {
	owner string
	repo  string
}

func resolveOwnerRepo(args map[string]any) (ownerRepo, bool) {
	owner, hasOwner := pickString(args, []string{"owner", "ownerLogin", "login"})
	repo, hasRepo := pickString(args, []string{"repo", "repoName", "repository"})
	if full, ok := pickString(args, []string{"full_name", "fullName", "repositoryFullName"}); ok && (!hasOwner || !hasRepo) && strings.Contains(full, "/") {
		parts := strings.SplitN(full, "/", 2)
		if !hasOwner {
			owner, hasOwner = parts[0], true
		}
		if !hasRepo {
			repo, hasRepo = parts[1], true
		}
	}
	if !hasOwner || !hasRepo {
		return ownerRepo{}, false
	}
	return ownerRepo{owner: owner, repo: repo}, true
}

// jsNumber converts a value the way JavaScript Number(value) does for the
// inputs a pull_number argument can carry.
func jsNumber(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		return jsNumberFromString(t)
	default:
		return math.NaN()
	}
}

func jsNumberFromString(s string) float64 {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0
	}
	switch trimmed {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(trimmed) > 2 && trimmed[0] == '0' && (trimmed[1] == 'x' || trimmed[1] == 'X') {
		if n, err := strconv.ParseInt(trimmed[2:], 16, 64); err == nil {
			return float64(n)
		}
		return math.NaN()
	}
	if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return f
	}
	return math.NaN()
}

func callTool(store *Store, rawName string, rawArgs any) (map[string]any, *rpcFailure) {
	args, _ := rawArgs.(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	// Match by suffix, case-insensitively, so a server prefix is accepted.
	wanted := strings.ToLower(rawName)
	var tool *toolDef
	for i := range mcpTools {
		t := &mcpTools[i]
		if wanted == t.name || strings.HasSuffix(wanted, "__"+t.name) || strings.HasSuffix(wanted, "/"+t.name) || strings.HasSuffix(wanted, ":"+t.name) {
			tool = t
			break
		}
	}
	if tool == nil {
		return nil, &rpcFailure{code: -32602, message: "unknown tool: " + rawName}
	}
	switch tool.name {
	case "create_repo":
		name, ok := pickString(args, []string{"name", "repo", "repository"})
		if !ok {
			return missingArg("name"), nil
		}
		if store.GetRepo(name) != nil {
			return toolText(map[string]any{"error": "repository " + name + " already exists"}, true), nil
		}
		description, _ := pickString(args, []string{"description"})
		private, _ := args["private"].(bool)
		repo := store.CreateRepo(name, description, private)
		return toolText(RepoJSON(store, repo), false), nil
	case "get_repo":
		target, ok := resolveOwnerRepo(args)
		if !ok {
			return missingArg("owner/repo"), nil
		}
		if target.owner != LOGIN {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		repo := store.GetRepo(target.repo)
		if repo == nil {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		return toolText(RepoJSON(store, repo), false), nil
	case "create_issue":
		target, ok := resolveOwnerRepo(args)
		if !ok {
			return missingArg("owner/repo"), nil
		}
		title, ok := pickString(args, []string{"title"})
		if !ok {
			return missingArg("title"), nil
		}
		if target.owner != LOGIN {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		repo := store.GetRepo(target.repo)
		if repo == nil {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		labelNames := []string{}
		if raw, present := args["labels"]; present {
			if names, valid := ParseLabelNames(raw, true); valid {
				labelNames = names
			}
		}
		var body *string
		if s, ok := args["body"].(string); ok {
			body = &s
		}
		created, errRes := CreateIssueRecord(store, repo, title, body, labelNames)
		if errRes != nil {
			return toolText(map[string]any{"error": "a named label does not exist"}, true), nil
		}
		return toolText(IssueJSON(store, repo, created, nil), false), nil
	case "list_issues":
		target, ok := resolveOwnerRepo(args)
		if !ok {
			return missingArg("owner/repo"), nil
		}
		if target.owner != LOGIN {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		repo := store.GetRepo(target.repo)
		if repo == nil {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		state, _ := pickString(args, []string{"state"})
		if state == "" {
			state = "open"
		}
		list := []any{}
		for _, issue := range repo.Issues {
			if state == "all" || string(issue.State) == state {
				list = append(list, IssueJSON(store, repo, issue, nil))
			}
		}
		return toolText(list, false), nil
	case "create_pull_request":
		target, ok := resolveOwnerRepo(args)
		if !ok {
			return missingArg("owner/repo"), nil
		}
		title, hasTitle := pickString(args, []string{"title"})
		head, hasHead := pickString(args, []string{"head", "headRef", "headRefName"})
		base, hasBase := pickString(args, []string{"base", "baseRef", "baseRefName"})
		if !hasTitle {
			return missingArg("title"), nil
		}
		if !hasHead {
			return missingArg("head"), nil
		}
		if !hasBase {
			return missingArg("base"), nil
		}
		if target.owner != LOGIN {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		repo := store.GetRepo(target.repo)
		if repo == nil {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		var body *string
		if s, ok := args["body"].(string); ok {
			body = &s
		}
		created, errRes := CreatePullRecord(store, repo, title, body, head, base)
		if errRes != nil {
			return toolText(map[string]any{"error": "could not create pull request"}, true), nil
		}
		return toolText(PRJSON(store, repo, created), false), nil
	case "merge_pull_request":
		target, ok := resolveOwnerRepo(args)
		if !ok {
			return missingArg("owner/repo"), nil
		}
		var rawNumber any
		for _, key := range []string{"pull_number", "pullNumber", "number"} {
			if v, present := args[key]; present && v != nil {
				rawNumber = v
				break
			}
		}
		number := jsNumber(rawNumber)
		if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
			return missingArg("pull_number"), nil
		}
		if target.owner != LOGIN {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		repo := store.GetRepo(target.repo)
		if repo == nil {
			return toolText(map[string]any{"error": "repository not found"}, true), nil
		}
		pull, ok := repo.PullByNumber[int(number)]
		if !ok {
			return toolText(map[string]any{"error": "pull request not found"}, true), nil
		}
		sha, mergeErr := MergePullRecord(store, repo, pull, nil, nil)
		if mergeErr != MergeErrNone {
			msg := "pull request already merged"
			if mergeErr == MergeErrConflict {
				msg = "merge conflict"
			}
			return toolText(map[string]any{"error": msg}, true), nil
		}
		return toolText(map[string]any{
			"sha":          sha,
			"merged":       true,
			"message":      "Pull request successfully merged",
			"pull_request": PRJSON(store, repo, pull),
		}, false), nil
	default:
		return nil, &rpcFailure{code: -32602, message: "unknown tool: " + rawName}
	}
}

// McpOutcome is one MCP HTTP answer.
type McpOutcome struct {
	Status  int
	JSON    any
	Headers map[string]string
	Empty   bool
}

func handleMcpOne(store *Store, msg any) map[string]any {
	req, ok := msg.(map[string]any)
	if !ok {
		return mcpRPCError(nil, -32600, "Invalid Request")
	}
	id, _ := req["id"]
	method, _ := req["method"].(string)
	if strings.HasPrefix(method, "notifications/") {
		return nil
	}
	params, _ := req["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	switch method {
	case "server/discover":
		versions := make([]any, 0, len(MCPVersions))
		for _, v := range MCPVersions {
			versions = append(versions, v)
		}
		return mcpResult(id, map[string]any{
			"protocolVersion":   "2026-07-28",
			"supportedVersions": versions,
			"serverInfo":        map[string]any{"name": "forge", "version": "1.0.0"},
			"capabilities":      map[string]any{"tools": map[string]any{}},
		})
	case "initialize":
		// A missing or non-string version negotiates to the
		// 2025-11-25 handshake default, as does any version
		// outside the supported list.
		requested, _ := params["protocolVersion"].(string)
		negotiated := "2025-11-25"
		for _, v := range MCPVersions {
			if v == requested {
				negotiated = requested
				break
			}
		}
		return mcpResult(id, map[string]any{
			"protocolVersion": negotiated,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "forge", "version": "1.0.0"},
		})
	case "ping":
		return mcpResult(id, map[string]any{})
	case "tools/list":
		parsed, err := unmarshalJSON(toolsListJSON)
		if err != nil {
			parsed = map[string]any{"tools": []any{}}
		}
		return mcpResult(id, parsed)
	case "tools/call":
		name, _ := params["name"].(string)
		outcome, failure := callTool(store, name, params["arguments"])
		if failure != nil {
			return mcpRPCError(id, failure.code, failure.message)
		}
		return mcpResult(id, outcome)
	case "resources/list":
		return mcpResult(id, map[string]any{"resources": []any{}})
	case "prompts/list":
		return mcpResult(id, map[string]any{"prompts": []any{}})
	case "resources/templates/list":
		return mcpResult(id, map[string]any{"resourceTemplates": []any{}})
	default:
		return mcpRPCError(id, -32601, "method not found: "+method)
	}
}

// HandleMcp executes one MCP request body: a single JSON-RPC message or
// a batch array.
func HandleMcp(store *Store, body any) McpOutcome {
	if arr, ok := body.([]any); ok {
		responses := []map[string]any{}
		for _, item := range arr {
			if res := handleMcpOne(store, item); res != nil {
				responses = append(responses, res)
			}
		}
		if len(responses) == 0 {
			return McpOutcome{Status: 202, Empty: true}
		}
		return McpOutcome{Status: 200, JSON: responses}
	}
	res := handleMcpOne(store, body)
	if res == nil {
		return McpOutcome{Status: 202, Empty: true}
	}
	outcome := McpOutcome{Status: 200, JSON: res}
	if req, ok := body.(map[string]any); ok {
		if method, _ := req["method"].(string); method == "initialize" {
			outcome.Headers = map[string]string{"mcp-session-id": "forge-session-1"}
		}
	}
	return outcome
}
