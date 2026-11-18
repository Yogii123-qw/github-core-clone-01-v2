package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testServer starts a forge server on an ephemeral port and returns its
// address plus a close function.
func testServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newForgeServer(NewStore(), "test-token")
	go server.Serve(listener)
	return listener.Addr().String(), func() { _ = listener.Close() }
}

// rawConn is a minimal HTTP/1.1 client over one TCP connection.
type rawConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialRaw(t *testing.T, addr string) *rawConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return &rawConn{conn: conn, br: bufio.NewReader(conn)}
}

func (c *rawConn) close() { _ = c.conn.Close() }

// readResponse reads one response: status line, headers, and the body
// framed by Content-Length.
func (c *rawConn) readResponse(t *testing.T) (int, map[string]string, []byte) {
	t.Helper()
	if err := c.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	statusLine, err := c.br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading status line: %v", err)
	}
	var version string
	var status int
	if _, err := fmt.Sscanf(statusLine, "%s %d", &version, &status); err != nil {
		t.Fatalf("parsing status line %q: %v", statusLine, err)
	}
	headers := map[string]string{}
	for {
		line, err := c.br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading headers: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		kv := strings.SplitN(line, ":", 2)
		headers[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1])
	}
	var body []byte
	if n, err := strconv.Atoi(headers["content-length"]); err == nil && n > 0 {
		body = make([]byte, n)
		if _, err := io.ReadFull(c.br, body); err != nil {
			t.Fatalf("reading body: %v", err)
		}
	}
	return status, headers, body
}

func (c *rawConn) request(t *testing.T, raw string) (int, map[string]string, []byte) {
	t.Helper()
	if _, err := c.conn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	return c.readResponse(t)
}

const authHeader = "Authorization: Bearer test-token\r\n"

func TestServerHealthzNoAuth(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	status, headers, body := c.request(t, "GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if status != 200 || string(body) != `{"ok":true}` {
		t.Fatalf("healthz = %d %q", status, body)
	}
	if headers["x-github-request-id"] == "" {
		t.Fatal("healthz response missing X-GitHub-Request-Id")
	}
	if headers["content-type"] != "application/json; charset=utf-8" {
		t.Fatalf("healthz content-type = %q", headers["content-type"])
	}
}

func TestServerAuth(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	// No token: 401.
	status, _, body := c.request(t, "GET /api/v3/user HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if status != 401 || !strings.Contains(string(body), "Requires authentication") {
		t.Fatalf("no-auth = %d %q", status, body)
	}
	c2 := dialRaw(t, addr)
	defer c2.close()
	// Wrong token: 401.
	status, _, _ = c2.request(t, "GET /api/v3/user HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer wrong\r\nConnection: close\r\n\r\n")
	if status != 401 {
		t.Fatalf("wrong-token = %d, want 401", status)
	}
	c3 := dialRaw(t, addr)
	defer c3.close()
	// Right token: 200.
	status, _, body = c3.request(t, "GET /api/v3/user HTTP/1.1\r\nHost: x\r\n"+authHeader+"Connection: close\r\n\r\n")
	if status != 200 || !strings.Contains(string(body), "adaiversen") {
		t.Fatalf("authed = %d %q", status, body)
	}
}

func TestServerMalformedJSONAnd404(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	// Malformed JSON body: 400.
	status, _, _ := c.request(t, "POST /api/v3/user/repos HTTP/1.1\r\nHost: x\r\n"+authHeader+"Content-Type: application/json\r\nContent-Length: 7\r\n\r\n{bad!!!")
	if status != 400 {
		t.Fatalf("malformed JSON = %d, want 400", status)
	}
	// Unknown route: 404 JSON.
	status, _, body := c.request(t, "GET /api/v3/nonexistent HTTP/1.1\r\nHost: x\r\n"+authHeader+"Connection: close\r\n\r\n")
	if status != 404 || !strings.Contains(string(body), "Not Found") {
		t.Fatalf("unknown route = %d %q", status, body)
	}
}

func TestServerKeepAliveReuse(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	// Three requests on one connection: ids must increase, proving the
	// same connection served them all.
	var ids []string
	for i := 0; i < 3; i++ {
		status, headers, _ := c.request(t, "GET /healthz HTTP/1.1\r\nHost: x\r\n\r\n")
		if status != 200 {
			t.Fatalf("request %d status = %d", i, status)
		}
		ids = append(ids, headers["x-github-request-id"])
	}
	if ids[0] != "req-1" || ids[1] != "req-2" || ids[2] != "req-3" {
		t.Fatalf("request ids = %v, want req-1..req-3", ids)
	}
}

func TestServerExpectContinue(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	// Send headers only, read the 100 interim, then send the body.
	if _, err := c.conn.Write([]byte("POST /api/v3/user/repos HTTP/1.1\r\nHost: x\r\n" + authHeader + "Content-Type: application/json\r\nContent-Length: 13\r\nExpect: 100-continue\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	interim, err := c.br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(interim, "100") {
		t.Fatalf("interim response = %q, want 100 Continue", interim)
	}
	// The interim response ends with an empty line.
	if _, err := c.br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := c.conn.Write([]byte(`{"name":"r1"}`)); err != nil {
		t.Fatal(err)
	}
	status, _, body := c.readResponse(t)
	if status != 201 || !strings.Contains(string(body), `"name":"r1"`) {
		t.Fatalf("expect-continue result = %d %q", status, body)
	}
}

func TestServerChunkedBody(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	body := `{"name":"r2"}`
	raw := "POST /api/v3/user/repos HTTP/1.1\r\nHost: x\r\n" + authHeader + "Content-Type: application/json\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n" +
		fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(body), body)
	status, _, resp := c.request(t, raw)
	if status != 201 || !strings.Contains(string(resp), `"name":"r2"`) {
		t.Fatalf("chunked result = %d %q", status, resp)
	}
}

func TestServerPipelined(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	raw := "GET /healthz HTTP/1.1\r\nHost: x\r\n\r\nGET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"
	if _, err := c.conn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		status, _, body := c.readResponse(t)
		if status != 200 || string(body) != `{"ok":true}` {
			t.Fatalf("pipelined response %d = %d %q", i, status, body)
		}
	}
}

func TestServerHTTP10(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	status, _, body := c.request(t, "GET /healthz HTTP/1.0\r\n\r\n")
	if status != 200 || string(body) != `{"ok":true}` {
		t.Fatalf("HTTP/1.0 = %d %q", status, body)
	}
}

func TestServerAbsoluteForm(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	c := dialRaw(t, addr)
	defer c.close()
	status, _, body := c.request(t, "GET http://example.com/healthz HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	if status != 200 || string(body) != `{"ok":true}` {
		t.Fatalf("absolute-form = %d %q", status, body)
	}
}

func TestServerConcurrentReads(t *testing.T) {
	addr, closeFn := testServer(t)
	defer closeFn()
	// 50 concurrent connections each issuing a GET, mirroring the
	// clone workload's concurrency shape at the HTTP layer.
	done := make(chan error, 50)
	for i := 0; i < 50; i++ {
		go func() {
			c := dialRaw(t, addr)
			defer c.close()
			status, _, body := c.request(t, "GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
			if status != 200 || string(body) != `{"ok":true}` {
				done <- fmt.Errorf("status=%d body=%q", status, body)
				return
			}
			done <- nil
		}()
	}
	for i := 0; i < 50; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
