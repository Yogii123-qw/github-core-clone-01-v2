// Entry <-> env lifecycle: bind an ephemeral port, register it with the
// suite, then poll until the suite says the trial is over and exit 0.
// The server itself lives in server.go; this file only owns the
// lifecycle.
//
// The suite's origin is MAKEFUN_ENV_URL. Without it there is no env to
// talk to, so the process exits 1 — unless FORGE_PORT is set, a
// local-development escape hatch that serves the forge standalone on
// that port forever (the trial always sets MAKEFUN_ENV_URL, so the
// hatch never triggers there).
package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const requestTimeout = 30 * time.Second
const retryInitial = 250 * time.Millisecond
const retryMax = 2 * time.Second

func retryDelay(attempt int) time.Duration {
	shift := attempt
	if shift > 8 {
		shift = 8
	}
	delay := retryInitial << shift
	if delay > retryMax {
		delay = retryMax
	}
	return delay
}

// register POSTs the bound port until the env accepts it (200, or 409
// when a first register won). A 400/405 answer is fatal.
func register(client *http.Client, envURL string, port int) error {
	payload := mustMarshalJSON(obj{"port": port})
	for attempt := 0; ; attempt++ {
		fatal, done := tryRegister(client, envURL, payload)
		if done {
			return nil
		}
		if fatal != nil {
			return fatal
		}
		time.Sleep(retryDelay(attempt))
	}
}

func tryRegister(client *http.Client, envURL string, payload []byte) (fatal error, done bool) {
	req, err := http.NewRequest(http.MethodPost, envURL+"/register", bytes.NewReader(payload))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict {
		return nil, true
	}
	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusMethodNotAllowed {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return fmt.Errorf("register answered HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(text))), false
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil, false
}

// waitForDone polls GET /done until it answers {"done": true}; it never
// returns before that.
func waitForDone(client *http.Client, envURL string) {
	for attempt := 0; ; attempt++ {
		if checkDone(client, envURL) {
			return
		}
		time.Sleep(retryDelay(attempt))
	}
}

func checkDone(client *http.Client, envURL string) bool {
	req, err := http.NewRequest(http.MethodGet, envURL+"/done", nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, res.Body)
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return false
	}
	parsed, err := unmarshalJSON(raw)
	if err != nil {
		return false
	}
	body, ok := parsed.(map[string]any)
	if !ok {
		return false
	}
	done, _ := body["done"].(bool)
	return done
}

func run() error {
	envURL := strings.TrimRight(os.Getenv("MAKEFUN_ENV_URL"), "/")
	standalonePort := os.Getenv("FORGE_PORT")

	server := newForgeServer(NewStore(), expectedToken())

	if envURL == "" && standalonePort != "" {
		port, err := strconv.Atoi(strings.TrimSpace(standalonePort))
		if err != nil {
			return fmt.Errorf("invalid FORGE_PORT %q: %v", standalonePort, err)
		}
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "[forge] standalone on port %d\n", port)
		return serveForever(server, listener)
	}
	if envURL == "" {
		fmt.Fprint(os.Stderr, "[forge] MAKEFUN_ENV_URL is not set: there is no env to register with\n")
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("the server did not bind an ephemeral TCP port")
	}
	port := tcpAddr.Port
	fmt.Fprintf(os.Stdout, "[forge] listening on port %d\n", port)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	client := &http.Client{Timeout: requestTimeout}
	if err := register(client, envURL, port); err != nil {
		_ = listener.Close()
		return err
	}
	fmt.Fprint(os.Stdout, "[forge] registered; waiting for the env to say done\n")
	done := make(chan struct{})
	go func() {
		waitForDone(client, envURL)
		close(done)
	}()
	select {
	case err := <-serveErr:
		if err != nil {
			return err
		}
		return fmt.Errorf("the server stopped before the env said done")
	case <-done:
	}
	_ = listener.Close()
	fmt.Fprint(os.Stdout, "[forge] done\n")
	return nil
}

// serveForever serves until the listener fails; it never returns nil.
func serveForever(server *forgeServer, listener net.Listener) error {
	return server.Serve(listener)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "[forge] %s\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
