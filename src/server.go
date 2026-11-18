// HTTP front door: one net/http server routing the four surfaces (git
// smart HTTP, REST v3, GraphQL, MCP), plus the /healthz boot probe. Auth
// is the bearer token in MAKEFUN_SHOP_TOKEN, required on every route
// except /healthz. Every reply is JSON (except git's binary protocol
// bodies) and carries a non-empty X-GitHub-Request-Id; errors carry a
// JSON body with a string message; malformed JSON bodies answer 400;
// unknown routes answer 404 JSON.
//
// Concurrency: one global Store mutex guards all mutable domain state.
// Read-only routes hold RLock, mutating routes hold Lock, for the whole
// handler execution; responses are fully rendered under the lock and
// written to the network after it is released, so a slow client never
// blocks the store.
package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

const jsonType = "application/json; charset=utf-8"
const maxBodyBytes = 256 * 1024 * 1024

// matchGitRoute parses "/{owner}/{repo}.git/{action}" where action is
// info/refs, git-upload-pack, or git-receive-pack. Owner and repo are
// single path segments. It mirrors the reference route pattern exactly:
// ".git/" cannot occur inside a segment, so the first occurrence is the
// separator.
func matchGitRoute(path string) (owner, repo, action string, ok bool) {
	if len(path) < 2 || path[0] != '/' {
		return "", "", "", false
	}
	rest := path[1:]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return "", "", "", false
	}
	owner = rest[:slash]
	rest = rest[slash+1:]
	dotGit := strings.Index(rest, ".git/")
	if dotGit <= 0 {
		return "", "", "", false
	}
	repo = rest[:dotGit]
	if strings.IndexByte(repo, '/') != -1 {
		return "", "", "", false
	}
	action = rest[dotGit+5:]
	if action != "info/refs" && action != "git-upload-pack" && action != "git-receive-pack" {
		return "", "", "", false
	}
	return owner, repo, action, true
}

// forgeServer is the HTTP front door. The zero value is not usable; use
// newForgeServer.
type forgeServer struct {
	store  *Store
	token  string
	reqSeq atomic.Int64
}

func newForgeServer(store *Store, token string) *forgeServer {
	return &forgeServer{store: store, token: token}
}

// expectedToken resolves the bearer token from MAKEFUN_SHOP_TOKEN.
// Empty means any non-empty presented token passes.
func expectedToken() string {
	return os.Getenv("MAKEFUN_SHOP_TOKEN")
}

func (s *forgeServer) nextRequestID() string {
	return fmt.Sprintf("req-%d", s.reqSeq.Add(1))
}

// responder writes exactly one response for one request.
type responder struct {
	srv   *forgeServer
	w     http.ResponseWriter
	wrote bool
}

// writeJSONBytes writes a JSON response. A nil payload writes headers
// only (204 and null-body answers), matching the reference behavior.
func (rs *responder) writeJSONBytes(status int, payload []byte, extra map[string]string) {
	id := rs.srv.nextRequestID()
	h := rs.w.Header()
	if payload != nil {
		h.Set("Content-Type", jsonType)
		h.Set("Content-Length", fmt.Sprintf("%d", len(payload)))
	}
	h.Set("X-GitHub-Request-Id", id)
	for k, v := range extra {
		h.Set(k, v)
	}
	rs.w.WriteHeader(status)
	if payload != nil {
		_, _ = rs.w.Write(payload)
	}
	rs.wrote = true
}

// writeJSON renders body and writes it as a JSON response.
func (rs *responder) writeJSON(status int, body any, extra map[string]string) {
	if status == 204 || body == nil {
		rs.writeJSONBytes(status, nil, extra)
		return
	}
	payload, err := marshalJSON(body)
	if err != nil {
		// Rendering a handler-built value cannot fail in practice; if it
		// ever does, answer 500 rather than a partial response.
		rs.writeJSONBytes(500, mustMarshalJSON(obj{"message": err.Error()}), nil)
		return
	}
	rs.writeJSONBytes(status, payload, extra)
}

func (rs *responder) writeGit(git GitResponse) {
	id := rs.srv.nextRequestID()
	h := rs.w.Header()
	h.Set("Content-Type", git.ContentType)
	h.Set("Content-Length", fmt.Sprintf("%d", len(git.Body)))
	h.Set("X-GitHub-Request-Id", id)
	for k, v := range git.ExtraHeaders {
		h.Set(k, v)
	}
	rs.w.WriteHeader(git.Status)
	_, _ = rs.w.Write(git.Body)
	rs.wrote = true
}

func (s *forgeServer) authed(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	presented := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if s.token == "" {
		return presented != ""
	}
	return presented == s.token
}

func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, errors.New("request body too large")
	}
	return body, nil
}

// decodeSegment decodes one path segment, falling back to the raw
// segment when the escape sequence is malformed.
func decodeSegment(seg string) string {
	if decoded, err := url.PathUnescape(seg); err == nil {
		return decoded
	}
	return seg
}

func parseQuery(rawQuery string) url.Values {
	values, _ := url.ParseQuery(rawQuery)
	if values == nil {
		values = url.Values{}
	}
	return values
}

// queryGet returns the first value for key, and whether it was present.
func queryGet(values url.Values, key string) (string, bool) {
	if vs, ok := values[key]; ok && len(vs) > 0 {
		return vs[0], true
	}
	return "", false
}

// mcpNeedsWriteLock reports whether an MCP request body contains a
// tools/call, which mutates the store.
func mcpNeedsWriteLock(body any) bool {
	if m, ok := body.(map[string]any); ok {
		return m["method"] == "tools/call"
	}
	if arr, ok := body.([]any); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok && m["method"] == "tools/call" {
				return true
			}
		}
	}
	return false
}

func (s *forgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rs := &responder{srv: s, w: w}
	defer func() {
		if rec := recover(); rec != nil {
			if !rs.wrote {
				rs.writeJSON(500, obj{"message": fmt.Sprint(rec)}, nil)
			}
		}
	}()
	s.dispatch(rs, r)
}

func (s *forgeServer) dispatch(rs *responder, r *http.Request) {
	method := r.Method
	rawURL := r.RequestURI
	if rawURL == "" {
		rawURL = r.URL.RequestURI()
	}
	qIndex := strings.Index(rawURL, "?")
	rawPath := rawURL
	rawQuery := ""
	if qIndex != -1 {
		rawPath = rawURL[:qIndex]
		rawQuery = rawURL[qIndex+1:]
	}
	path := rawPath
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}

	// Boot probe: no auth.
	if method == "GET" && path == "/healthz" {
		rs.writeJSON(200, obj{"ok": true}, nil)
		return
	}

	if !s.authed(r) {
		rs.writeJSON(401, obj{"message": "Requires authentication", "documentation_url": "https://docs.github.com/rest"}, nil)
		return
	}

	// Git smart HTTP: /{owner}/{repo}.git/info/refs | /git-upload-pack | /git-receive-pack
	if owner, repoName, action, ok := matchGitRoute(path); ok {
		owner = decodeSegment(owner)
		repoName = decodeSegment(repoName)
		if action == "info/refs" {
			if method != "GET" {
				rs.writeJSON(405, obj{"message": "Method Not Allowed"}, nil)
				return
			}
			query := parseQuery(rawQuery)
			service, _ := queryGet(query, "service")
			gitProtocol := r.Header.Get("Git-Protocol")
			s.store.RLock()
			var repo *Repo
			if owner == LOGIN {
				repo = s.store.GetRepo(repoName)
			}
			git := HandleInfoRefs(repo, service, gitProtocol)
			s.store.RUnlock()
			rs.writeGit(git)
			return
		}
		isReceive := action == "git-receive-pack"
		if isReceive {
			s.store.Lock()
		} else {
			s.store.RLock()
		}
		var repo *Repo
		if owner == LOGIN {
			repo = s.store.GetRepo(repoName)
		}
		if repo == nil {
			if isReceive {
				s.store.Unlock()
			} else {
				s.store.RUnlock()
			}
			rs.writeJSON(404, obj{"message": "Not Found"}, nil)
			return
		}
		if method != "POST" {
			if isReceive {
				s.store.Unlock()
			} else {
				s.store.RUnlock()
			}
			rs.writeJSON(405, obj{"message": "Method Not Allowed"}, nil)
			return
		}
		// The body is read while holding the lock: receive-pack bodies
		// are small, upload-pack bodies are smaller, and holding RLock
		// keeps concurrent clones parallel.
		body, err := readBody(r)
		if err != nil {
			if isReceive {
				s.store.Unlock()
			} else {
				s.store.RUnlock()
			}
			rs.writeJSON(500, obj{"message": err.Error()}, nil)
			return
		}
		var git GitResponse
		if action == "git-upload-pack" {
			if r.Header.Get("Git-Protocol") == "version=2" {
				git = HandleUploadPackV2(repo, body)
			} else {
				git = HandleUploadPackV0(repo, body)
			}
		} else {
			git = HandleReceivePack(repo, body)
		}
		if isReceive {
			s.store.Unlock()
		} else {
			s.store.RUnlock()
		}
		rs.writeGit(git)
		return
	}

	// GraphQL.
	if path == "/api/graphql" {
		if method == "GET" {
			query := parseQuery(rawQuery)
			var qVal any
			if q, ok := queryGet(query, "query"); ok {
				qVal = q
			}
			var variables any = map[string]any{}
			if rawVars, ok := queryGet(query, "variables"); ok {
				parsed, err := unmarshalJSON([]byte(rawVars))
				if err != nil {
					rs.writeJSON(400, obj{"message": "Problems parsing JSON"}, nil)
					return
				}
				variables = parsed
			}
			var opName any
			if name, ok := queryGet(query, "operationName"); ok {
				opName = name
			}
			s.store.RLock()
			out, err := HandleGraphQL(s.store, map[string]any{"query": qVal, "variables": variables, "operationName": opName})
			s.store.RUnlock()
			if err != nil {
				rs.writeJSON(500, obj{"message": err.Error()}, nil)
				return
			}
			rs.writeJSON(out.Status, out.JSON, nil)
			return
		}
		if method != "POST" {
			rs.writeJSON(405, obj{"message": "Method Not Allowed"}, nil)
			return
		}
		body, err := readBody(r)
		if err != nil {
			rs.writeJSON(500, obj{"message": err.Error()}, nil)
			return
		}
		if len(body) == 0 {
			rs.writeJSON(400, obj{"message": "Problems parsing JSON"}, nil)
			return
		}
		parsed, err := unmarshalJSON(body)
		if err != nil {
			rs.writeJSON(400, obj{"message": "Problems parsing JSON"}, nil)
			return
		}
		s.store.Lock()
		out, handleErr := HandleGraphQL(s.store, parsed)
		s.store.Unlock()
		if handleErr != nil {
			rs.writeJSON(500, obj{"message": handleErr.Error()}, nil)
			return
		}
		rs.writeJSON(out.Status, out.JSON, nil)
		return
	}

	// MCP.
	if path == "/mcp" {
		if method != "POST" {
			rs.writeJSON(405, obj{"message": "Method Not Allowed"}, nil)
			return
		}
		body, err := readBody(r)
		if err != nil {
			rs.writeJSON(500, obj{"message": err.Error()}, nil)
			return
		}
		parsed, err := unmarshalJSON(body)
		if err != nil {
			rs.writeJSON(400, obj{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   obj{"code": -32700, "message": "Parse error"},
			}, nil)
			return
		}
		if mcpNeedsWriteLock(parsed) {
			s.store.Lock()
			out := HandleMcp(s.store, parsed)
			s.store.Unlock()
			s.writeMcpOutcome(rs, out)
		} else {
			s.store.RLock()
			out := HandleMcp(s.store, parsed)
			s.store.RUnlock()
			s.writeMcpOutcome(rs, out)
		}
		return
	}

	// REST v3.
	if path == "/api/v3" || strings.HasPrefix(path, "/api/v3/") {
		restPath := strings.TrimPrefix(path, "/api/v3")
		var segments []string
		for _, seg := range strings.Split(restPath, "/") {
			if seg != "" {
				segments = append(segments, decodeSegment(seg))
			}
		}
		var parsed any
		hasBody := false
		if method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE" {
			body, err := readBody(r)
			if err != nil {
				rs.writeJSON(500, obj{"message": err.Error()}, nil)
				return
			}
			if len(body) > 0 {
				hasBody = true
				parsed, err = unmarshalJSON(body)
				if err != nil {
					rs.writeJSON(400, obj{"message": "Problems parsing JSON"}, nil)
					return
				}
			}
		}
		host := r.Host
		if host == "" {
			host = "127.0.0.1"
		}
		restCtx := &RestContext{
			Method:   method,
			Segments: segments,
			Query:    parseQuery(rawQuery),
			Body:     parsed,
			HasBody:  hasBody,
			Store:    s.store,
			Host:     host,
		}
		isRead := method == "GET"
		if isRead {
			s.store.RLock()
		} else {
			s.store.Lock()
		}
		out := HandleRest(restCtx)
		headers := map[string]string{"x-oauth-scopes": "repo, read:org"}
		for k, v := range out.Headers {
			headers[k] = v
		}
		var payload []byte
		useRaw := out.Raw != nil
		if useRaw {
			payload = out.Raw
		} else if out.Status != 204 && out.JSON != nil {
			rendered, err := marshalJSON(out.JSON)
			if err != nil {
				if isRead {
					s.store.RUnlock()
				} else {
					s.store.Unlock()
				}
				rs.writeJSON(500, obj{"message": err.Error()}, nil)
				return
			}
			payload = rendered
		}
		status := out.Status
		if isRead {
			s.store.RUnlock()
		} else {
			s.store.Unlock()
		}
		rs.writeJSONBytes(status, payload, headers)
		return
	}

	rs.writeJSON(404, obj{"message": "Not Found"}, nil)
}

func (s *forgeServer) writeMcpOutcome(rs *responder, out McpOutcome) {
	if out.Empty {
		rs.writeJSONBytes(out.Status, nil, out.Headers)
		return
	}
	rs.writeJSON(out.Status, out.JSON, out.Headers)
}
