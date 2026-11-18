// HTTP front door: a raw-TCP HTTP/1.1 server routing the four surfaces
// (git smart HTTP, REST v3, GraphQL, MCP), plus the /healthz boot probe.
// Auth is the bearer token in MAKEFUN_SHOP_TOKEN, required on every
// route except /healthz. Every reply is JSON (except git's binary
// protocol bodies) and carries a non-empty X-GitHub-Request-Id; errors
// carry a JSON body with a string message; malformed JSON bodies answer
// 400; unknown routes answer 404 JSON.
//
// The transport is deliberately hand-rolled: one goroutine per
// connection, requests parsed from a reused buffer, responses built
// into one buffer and written once. This keeps the per-request cost to
// the request and response syscalls plus the handler work, with no
// header-map, timer, or allocation overhead on the hot path. The wire
// semantics match a conventional HTTP/1.1 server: keep-alive by
// default on HTTP/1.1, Connection: close honored, Expect: 100-continue
// answered before the body is read, Content-Length and chunked request
// bodies accepted, and a Date header on every response.
//
// Concurrency: one global Store mutex guards all mutable domain state.
// Read-only routes hold RLock, mutating routes hold Lock, for the whole
// handler execution; responses are fully rendered under the lock and
// written to the network after it is released, so a slow client never
// blocks the store. Locks are always released with defer, so a handler
// panic can never wedge the store.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const jsonType = "application/json; charset=utf-8"
const maxBodyBytes = 256 * 1024 * 1024
const maxHeaderLineBytes = 32 * 1024
const maxHeaderBlockBytes = 64 * 1024
const maxRequestLineBytes = 32 * 1024
const connReadBufferBytes = 64 * 1024

const firstRequestTimeout = 60 * time.Second
const keepAliveIdleTimeout = 30 * time.Second

var errBodyTooLarge = errors.New("request body too large")
var errMalformedRequest = errors.New("malformed HTTP request")

var reasonPhrases = map[int]string{
	200: "OK",
	201: "Created",
	202: "Accepted",
	204: "No Content",
	400: "Bad Request",
	401: "Unauthorized",
	404: "Not Found",
	405: "Method Not Allowed",
	500: "Internal Server Error",
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

// lockFor acquires the store lock for a read-only (write=false) or
// mutating (write=true) route and returns its release function. Callers
// always defer the release so a handler panic cannot wedge the store.
func (s *forgeServer) lockFor(write bool) func() {
	if write {
		s.store.Lock()
		return s.store.Unlock
	}
	s.store.RLock()
	return s.store.RUnlock
}

// ---------------------------------------------------------------------------
// Request parsing
// ---------------------------------------------------------------------------

// rawRequest is one parsed HTTP/1.1 request. Only the headers the
// dispatcher reads are kept; the rest are skipped during parsing.
type rawRequest struct {
	method        string
	target        string
	version       string // "1.0" or "1.1"
	host          string
	auth          string
	gitProtocol   string
	body          []byte
	closeAfter    bool
	keepAliveHint bool // Connection: keep-alive (matters for HTTP/1.0)
}

// pathAndQuery splits the request target into its path and raw query
// parts. Absolute-form targets are reduced to origin-form first.
func (req *rawRequest) pathAndQuery() (path, rawQuery string) {
	target := req.target
	if i := strings.Index(target, "://"); i != -1 && i < 10 {
		// Absolute-form: scheme://authority/path?query.
		rest := target[i+3:]
		if slash := strings.IndexByte(rest, '/'); slash != -1 {
			target = rest[slash:]
		} else {
			target = "/"
		}
	}
	if q := strings.IndexByte(target, '?'); q != -1 {
		return target[:q], target[q+1:]
	}
	return target, ""
}

// readCappedLine reads one LF-terminated line, enforcing a byte cap so a
// client that never sends a newline cannot grow memory without bound.
// The trailing CRLF or LF is stripped.
func readCappedLine(br *bufio.Reader, limit int) (string, error) {
	var buf []byte
	for {
		frag, err := br.ReadSlice('\n')
		if len(frag) > 0 {
			if len(buf)+len(frag) > limit {
				return "", errMalformedRequest
			}
			buf = append(buf, frag...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}
	line := string(buf)
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

// readRequest parses one request from the connection: request line,
// headers, an interim 100-continue answer when asked, and the body.
func readRequest(br *bufio.Reader, w io.Writer) (*rawRequest, error) {
	line, err := readCappedLine(br, maxRequestLineBytes)
	if err != nil {
		return nil, err
	}
	sp1 := strings.IndexByte(line, ' ')
	sp2 := strings.LastIndexByte(line, ' ')
	if sp1 <= 0 || sp2 <= sp1+1 {
		return nil, errMalformedRequest
	}
	versionToken := line[sp2+1:]
	if versionToken != "HTTP/1.1" && versionToken != "HTTP/1.0" {
		return nil, errMalformedRequest
	}
	req := &rawRequest{
		method:  line[:sp1],
		target:  line[sp1+1 : sp2],
		version: versionToken[5:],
	}
	if req.target == "" {
		return nil, errMalformedRequest
	}

	var contentLength int64 = -1
	chunked := false
	expectContinue := false
	headerBytes := 0
	for {
		headerLine, err := readCappedLine(br, maxHeaderLineBytes)
		if err != nil {
			return nil, err
		}
		headerBytes += len(headerLine) + 2
		if headerBytes > maxHeaderBlockBytes {
			return nil, errMalformedRequest
		}
		if headerLine == "" {
			break
		}
		colon := strings.IndexByte(headerLine, ':')
		if colon <= 0 {
			return nil, errMalformedRequest
		}
		name := strings.ToLower(strings.TrimSpace(headerLine[:colon]))
		value := strings.TrimSpace(headerLine[colon+1:])
		switch name {
		case "authorization":
			if req.auth == "" {
				req.auth = value
			}
		case "git-protocol":
			if req.gitProtocol == "" {
				req.gitProtocol = value
			}
		case "host":
			if req.host == "" {
				req.host = value
			}
		case "content-length":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return nil, errMalformedRequest
			}
			if contentLength != -1 && contentLength != n {
				return nil, errMalformedRequest
			}
			contentLength = n
		case "transfer-encoding":
			for _, token := range strings.Split(strings.ToLower(value), ",") {
				if strings.TrimSpace(token) == "chunked" {
					chunked = true
				}
			}
		case "expect":
			if strings.EqualFold(value, "100-continue") {
				expectContinue = true
			}
		case "connection":
			for _, token := range strings.Split(strings.ToLower(value), ",") {
				switch strings.TrimSpace(token) {
				case "close":
					req.closeAfter = true
				case "keep-alive":
					req.keepAliveHint = true
				}
			}
		}
	}

	if expectContinue {
		if _, err := io.WriteString(w, "HTTP/1.1 100 Continue\r\n\r\n"); err != nil {
			return nil, err
		}
	}

	if chunked {
		body, err := readChunkedBody(br)
		if err != nil {
			return nil, err
		}
		req.body = body
	} else if contentLength > 0 {
		if contentLength > maxBodyBytes {
			return nil, errBodyTooLarge
		}
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
		req.body = body
	} else {
		req.body = nil
	}
	return req, nil
}

// readChunkedBody decodes a chunked request body, enforcing the body
// size cap as chunks arrive.
func readChunkedBody(br *bufio.Reader) ([]byte, error) {
	var body []byte
	for {
		sizeLine, err := readCappedLine(br, maxHeaderLineBytes)
		if err != nil {
			return nil, err
		}
		if i := strings.IndexByte(sizeLine, ';'); i != -1 {
			sizeLine = sizeLine[:i]
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeLine), 16, 64)
		if err != nil || size < 0 {
			return nil, errMalformedRequest
		}
		if size == 0 {
			for {
				trailer, err := readCappedLine(br, maxHeaderLineBytes)
				if err != nil {
					return nil, err
				}
				if trailer == "" {
					return body, nil
				}
			}
		}
		if int64(len(body))+size > maxBodyBytes {
			return nil, errBodyTooLarge
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, err
		}
		body = append(body, chunk...)
		crlf, err := readCappedLine(br, maxHeaderLineBytes)
		if err != nil {
			return nil, err
		}
		if crlf != "" {
			return nil, errMalformedRequest
		}
	}
}

// ---------------------------------------------------------------------------
// Response writing
// ---------------------------------------------------------------------------

// rawResponse is one dispatch result, fully rendered before the network
// write. A nil payload means headers only; every status except 204 then
// carries Content-Length: 0 so a keep-alive client knows the response
// is complete.
type rawResponse struct {
	status      int
	contentType string
	payload     []byte
	extra       map[string]string
}

func jsonResponse(status int, body any) rawResponse {
	if status == 204 || body == nil {
		return rawResponse{status: status}
	}
	payload, err := marshalJSON(body)
	if err != nil {
		return rawResponse{status: 500, contentType: jsonType, payload: mustMarshalJSON(obj{"message": err.Error()})}
	}
	return rawResponse{status: status, contentType: jsonType, payload: payload}
}

var dateCache atomic.Value // string, the current Date header value
var dateCacheSec atomic.Int64

func httpDate() string {
	now := time.Now().Unix()
	if dateCacheSec.Load() == now {
		if v := dateCache.Load(); v != nil {
			return v.(string)
		}
	}
	s := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	dateCache.Store(s)
	dateCacheSec.Store(now)
	return s
}

// writeRawResponse serializes one response: status line, Date,
// Content-Type, Content-Length, the request id, extras, and the body.
// Small responses go out in a single write; large bodies follow their
// headers in a second write to avoid an extra copy.
func (s *forgeServer) writeRawResponse(w io.Writer, req *rawRequest, resp rawResponse) error {
	var head []byte
	head = append(head, "HTTP/"...)
	head = append(head, req.version...)
	head = append(head, ' ')
	head = append(head, strconv.Itoa(resp.status)...)
	head = append(head, ' ')
	if phrase, ok := reasonPhrases[resp.status]; ok {
		head = append(head, phrase...)
	}
	head = append(head, "\r\nDate: "...)
	head = append(head, httpDate()...)
	head = append(head, "\r\n"...)
	if resp.contentType != "" {
		head = append(head, "Content-Type: "...)
		head = append(head, resp.contentType...)
		head = append(head, "\r\n"...)
	}
	if resp.payload != nil {
		head = append(head, "Content-Length: "...)
		head = append(head, strconv.Itoa(len(resp.payload))...)
		head = append(head, "\r\n"...)
	} else if resp.status != 204 {
		head = append(head, "Content-Length: 0\r\n"...)
	}
	head = append(head, "X-GitHub-Request-Id: "...)
	head = append(head, s.nextRequestID()...)
	head = append(head, "\r\n"...)
	for k, v := range resp.extra {
		head = append(head, k...)
		head = append(head, ": "...)
		head = append(head, v...)
		head = append(head, "\r\n"...)
	}
	if req.closeAfter && req.version == "1.1" {
		head = append(head, "Connection: close\r\n"...)
	} else if req.version == "1.0" && req.keepAliveHint && !req.closeAfter {
		head = append(head, "Connection: keep-alive\r\n"...)
	}
	head = append(head, "\r\n"...)
	if len(resp.payload) < 65536 {
		buf := make([]byte, 0, len(head)+len(resp.payload))
		buf = append(buf, head...)
		buf = append(buf, resp.payload...)
		_, err := w.Write(buf)
		return err
	}
	if _, err := w.Write(head); err != nil {
		return err
	}
	_, err := w.Write(resp.payload)
	return err
}

// ---------------------------------------------------------------------------
// Connection serving
// ---------------------------------------------------------------------------

// Serve accepts connections until the listener closes, serving each on
// its own goroutine.
func (s *forgeServer) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.serveConn(conn)
	}
}

func (s *forgeServer) serveConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, connReadBufferBytes)
	first := true
	for {
		timeout := keepAliveIdleTimeout
		if first {
			timeout = firstRequestTimeout
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		req, err := readRequest(br, conn)
		if err != nil {
			if errors.Is(err, errBodyTooLarge) {
				// The unread remainder corrupts the stream, so the
				// connection closes after this answer.
				_ = conn.SetReadDeadline(time.Time{})
				resp := jsonResponse(500, obj{"message": errBodyTooLarge.Error()})
				_ = s.writeRawResponse(conn, &rawRequest{version: "1.1"}, resp)
			}
			return
		}
		first = false
		// No timeout on the request itself once its headers arrived.
		_ = conn.SetReadDeadline(time.Time{})
		resp := s.handleRawSafe(req)
		if err := s.writeRawResponse(conn, req, resp); err != nil {
			return
		}
		if req.closeAfter || (req.version == "1.0" && !req.keepAliveHint) {
			return
		}
	}
}

// handleRawSafe runs the dispatcher, turning a handler panic into a 500
// JSON answer. Store locks are released by defer inside handleRaw, so a
// panic never wedges the store.
func (s *forgeServer) handleRawSafe(req *rawRequest) (resp rawResponse) {
	defer func() {
		if rec := recover(); rec != nil {
			resp = jsonResponse(500, obj{"message": fmt.Sprint(rec)})
		}
	}()
	return s.handleRaw(req)
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

func (s *forgeServer) authed(req *rawRequest) bool {
	header := req.auth
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	presented := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if s.token == "" {
		return presented != ""
	}
	return presented == s.token
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

func gitResponse(git GitResponse) rawResponse {
	extra := make(map[string]string, len(git.ExtraHeaders))
	for k, v := range git.ExtraHeaders {
		extra[k] = v
	}
	return rawResponse{status: git.Status, contentType: git.ContentType, payload: git.Body, extra: extra}
}

func (s *forgeServer) handleRaw(req *rawRequest) rawResponse {
	method := req.method
	path, rawQuery := req.pathAndQuery()
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}

	// Boot probe: no auth.
	if method == "GET" && path == "/healthz" {
		return jsonResponse(200, obj{"ok": true})
	}

	if !s.authed(req) {
		return jsonResponse(401, obj{"message": "Requires authentication", "documentation_url": "https://docs.github.com/rest"})
	}

	// Git smart HTTP: /{owner}/{repo}.git/info/refs | /git-upload-pack | /git-receive-pack
	if owner, repoName, action, ok := matchGitRoute(path); ok {
		owner = decodeSegment(owner)
		repoName = decodeSegment(repoName)
		if action == "info/refs" {
			if method != "GET" {
				return jsonResponse(405, obj{"message": "Method Not Allowed"})
			}
			query := parseQuery(rawQuery)
			service, _ := queryGet(query, "service")
			defer s.lockFor(false)()
			var repo *Repo
			if owner == LOGIN {
				repo = s.store.GetRepo(repoName)
			}
			return gitResponse(HandleInfoRefs(repo, service, req.gitProtocol))
		}
		isReceive := action == "git-receive-pack"
		defer s.lockFor(isReceive)()
		var repo *Repo
		if owner == LOGIN {
			repo = s.store.GetRepo(repoName)
		}
		if repo == nil {
			return jsonResponse(404, obj{"message": "Not Found"})
		}
		if method != "POST" {
			return jsonResponse(405, obj{"message": "Method Not Allowed"})
		}
		if action == "git-upload-pack" {
			if req.gitProtocol == "version=2" {
				return gitResponse(HandleUploadPackV2(repo, req.body))
			}
			return gitResponse(HandleUploadPackV0(repo, req.body))
		}
		return gitResponse(HandleReceivePack(repo, req.body))
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
					return jsonResponse(400, obj{"message": "Problems parsing JSON"})
				}
				variables = parsed
			}
			var opName any
			if name, ok := queryGet(query, "operationName"); ok {
				opName = name
			}
			defer s.lockFor(false)()
			out, err := HandleGraphQL(s.store, map[string]any{"query": qVal, "variables": variables, "operationName": opName})
			if err != nil {
				return jsonResponse(500, obj{"message": err.Error()})
			}
			return jsonResponse(out.Status, out.JSON)
		}
		if method != "POST" {
			return jsonResponse(405, obj{"message": "Method Not Allowed"})
		}
		if len(req.body) == 0 {
			return jsonResponse(400, obj{"message": "Problems parsing JSON"})
		}
		parsed, err := unmarshalJSON(req.body)
		if err != nil {
			return jsonResponse(400, obj{"message": "Problems parsing JSON"})
		}
		defer s.lockFor(true)()
		out, handleErr := HandleGraphQL(s.store, parsed)
		if handleErr != nil {
			return jsonResponse(500, obj{"message": handleErr.Error()})
		}
		return jsonResponse(out.Status, out.JSON)
	}

	// MCP.
	if path == "/mcp" {
		if method != "POST" {
			return jsonResponse(405, obj{"message": "Method Not Allowed"})
		}
		parsed, err := unmarshalJSON(req.body)
		if err != nil {
			return jsonResponse(400, obj{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   obj{"code": -32700, "message": "Parse error"},
			})
		}
		defer s.lockFor(mcpNeedsWriteLock(parsed))()
		out := HandleMcp(s.store, parsed)
		if out.Empty {
			return rawResponse{status: out.Status, extra: out.Headers}
		}
		resp := jsonResponse(out.Status, out.JSON)
		resp.extra = out.Headers
		return resp
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
			if len(req.body) > 0 {
				hasBody = true
				var err error
				parsed, err = unmarshalJSON(req.body)
				if err != nil {
					return jsonResponse(400, obj{"message": "Problems parsing JSON"})
				}
			}
		}
		host := req.host
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
		defer s.lockFor(method != "GET")()
		out := HandleRest(restCtx)
		headers := map[string]string{"x-oauth-scopes": "repo, read:org"}
		for k, v := range out.Headers {
			headers[k] = v
		}
		if out.Raw != nil {
			return rawResponse{status: out.Status, contentType: jsonType, payload: out.Raw, extra: headers}
		}
		resp := jsonResponse(out.Status, out.JSON)
		resp.extra = headers
		return resp
	}

	return jsonResponse(404, obj{"message": "Not Found"})
}
