package main

// LSP proxy between a client that only understands push diagnostics (micro's lsp
// plugin) and a server that delivers them by pull (tsgo, TypeScript rewritten in
// Go).
//
// Besides forwarding messages, it:
//   - announces to the server the pull capabilities the client does not announce;
//   - answers the server's requests on its own (workspace/configuration and
//     client/registerCapability), which block tsgo until they get a reply;
//   - on every didOpen/didChange/didSave asks for textDocument/diagnostic and
//     converts the reply into textDocument/publishDiagnostics for the client.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Prefix for the ids created here. Client ids are numbers, so a prefixed string
// never collides with them.
const diagIDPrefix = "nani-diag-"

// debounce between the last edit and the diagnostics request.
const diagDebounce = 300 * time.Millisecond

type message map[string]any

type proxy struct {
	toServer  *bufio.Writer
	toClient  *bufio.Writer
	serverMu  sync.Mutex
	clientMu  sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]string // diagnostics request id -> uri
	timersMu  sync.Mutex
	timers    map[string]*time.Timer
	diagSeq   int
	debugOut  io.Writer
}

func newProxy(toServer, toClient io.Writer, debug io.Writer) *proxy {
	return &proxy{
		toServer: bufio.NewWriter(toServer),
		toClient: bufio.NewWriter(toClient),
		pending:  map[string]string{},
		timers:   map[string]*time.Timer{},
		debugOut: debug,
	}
}

func (p *proxy) logf(format string, args ...any) {
	if p.debugOut == nil {
		return
	}
	fmt.Fprintf(p.debugOut, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
}

// readMessage reads one LSP frame: headers, blank line, N-byte body.
func readMessage(r *bufio.Reader) (message, []byte, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				length, err = strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return nil, nil, fmt.Errorf("invalid Content-Length: %w", err)
				}
			}
		}
	}
	if length <= 0 {
		return nil, nil, fmt.Errorf("frame without Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, nil, err
	}
	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		// Forward the raw body: failing to understand a message is no reason to
		// drop the connection.
		return nil, body, nil
	}
	return msg, body, nil
}

func writeFrame(mu *sync.Mutex, w *bufio.Writer, body []byte) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return w.Flush()
}

func (p *proxy) sendToServer(msg message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return writeFrame(&p.serverMu, p.toServer, body)
}

func (p *proxy) sendToClient(msg message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return writeFrame(&p.clientMu, p.toClient, body)
}

// addPullCapabilities tells the server we support pull diagnostics and
// workspace/configuration, which the proxy answers on the client's behalf.
func addPullCapabilities(msg message) {
	params, ok := msg["params"].(map[string]any)
	if !ok {
		return
	}
	caps, ok := params["capabilities"].(map[string]any)
	if !ok {
		caps = map[string]any{}
		params["capabilities"] = caps
	}
	td, ok := caps["textDocument"].(map[string]any)
	if !ok {
		td = map[string]any{}
		caps["textDocument"] = td
	}
	td["diagnostic"] = map[string]any{
		"dynamicRegistration":    true,
		"relatedDocumentSupport": false,
	}
	if _, ok := td["synchronization"]; !ok {
		td["synchronization"] = map[string]any{"didSave": true, "dynamicRegistration": false}
	}
	ws, ok := caps["workspace"].(map[string]any)
	if !ok {
		ws = map[string]any{}
		caps["workspace"] = ws
	}
	ws["configuration"] = true
	ws["workspaceFolders"] = true
	ws["didChangeConfiguration"] = map[string]any{"dynamicRegistration": true}
}

func uriOf(msg message) string {
	params, ok := msg["params"].(map[string]any)
	if !ok {
		return ""
	}
	doc, ok := params["textDocument"].(map[string]any)
	if !ok {
		return ""
	}
	uri, _ := doc["uri"].(string)
	return uri
}

// scheduleDiagnostics asks for the uri's diagnostics after the debounce, resetting
// the timer on every new edit of the same file.
func (p *proxy) scheduleDiagnostics(uri string) {
	if uri == "" {
		return
	}
	p.timersMu.Lock()
	defer p.timersMu.Unlock()
	if t, ok := p.timers[uri]; ok {
		t.Stop()
	}
	p.timers[uri] = time.AfterFunc(diagDebounce, func() {
		p.requestDiagnostics(uri)
	})
}

func (p *proxy) requestDiagnostics(uri string) {
	p.pendingMu.Lock()
	p.diagSeq++
	id := diagIDPrefix + strconv.Itoa(p.diagSeq)
	p.pending[id] = uri
	p.pendingMu.Unlock()

	p.logf("-> requesting diagnostics for %s (id %s)", uri, id)
	err := p.sendToServer(message{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "textDocument/diagnostic",
		"params":  map[string]any{"textDocument": map[string]any{"uri": uri}},
	})
	if err != nil {
		p.logf("failed to request diagnostics: %v", err)
	}
}

// handleDiagnosticResponse turns the pull reply into a push notification. Returns
// false when the message is not a reply of ours.
func (p *proxy) handleDiagnosticResponse(msg message) bool {
	id, ok := msg["id"].(string)
	if !ok || !strings.HasPrefix(id, diagIDPrefix) {
		return false
	}
	p.pendingMu.Lock()
	uri := p.pending[id]
	delete(p.pending, id)
	p.pendingMu.Unlock()

	// A server without pull support answers with an error. Publishing an empty
	// list here would wipe the diagnostics it already pushed.
	if e, ok := msg["error"]; ok {
		p.logf("server refused pull diagnostics (%v); keeping the pushed ones", e)
		return true
	}

	items := []any{}
	if result, ok := msg["result"].(map[string]any); ok {
		if got, ok := result["items"].([]any); ok {
			items = got
		}
	}
	p.logf("<- %d diagnostic(s) for %s", len(items), uri)
	err := p.sendToClient(message{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params":  map[string]any{"uri": uri, "diagnostics": items},
	})
	if err != nil {
		p.logf("failed to publish diagnostics: %v", err)
	}
	return true
}

// answerServerRequest replies to the requests the client does not know how to
// answer. tsgo stays blocked until it gets them.
func (p *proxy) answerServerRequest(msg message) bool {
	method, _ := msg["method"].(string)
	if _, hasID := msg["id"]; !hasID {
		return false
	}
	var result any
	switch method {
	case "workspace/configuration":
		// One empty object per requested item: tsgo falls back to its defaults.
		n := 1
		if params, ok := msg["params"].(map[string]any); ok {
			if items, ok := params["items"].([]any); ok {
				n = len(items)
			}
		}
		out := make([]any, n)
		for i := range out {
			out[i] = map[string]any{}
		}
		result = out
	case "client/registerCapability", "client/unregisterCapability",
		"workspace/semanticTokens/refresh", "workspace/diagnostic/refresh",
		"workspace/codeLens/refresh", "workspace/inlayHint/refresh":
		result = nil
	default:
		return false
	}
	p.logf("answering server request: %s", method)
	if err := p.sendToServer(message{"jsonrpc": "2.0", "id": msg["id"], "result": result}); err != nil {
		p.logf("failed to answer %s: %v", method, err)
	}
	return true
}

// clientToServer forwards what comes from the editor, watching the edits.
func (p *proxy) clientToServer(in io.Reader) error {
	r := bufio.NewReaderSize(in, 1<<20)
	for {
		msg, body, err := readMessage(r)
		if err != nil {
			return err
		}
		if msg == nil {
			if err := writeFrame(&p.serverMu, p.toServer, body); err != nil {
				return err
			}
			continue
		}
		method, _ := msg["method"].(string)
		switch method {
		case "initialize":
			addPullCapabilities(msg)
			p.logf("client initialize, pull capabilities injected")
		case "textDocument/didOpen", "textDocument/didChange", "textDocument/didSave":
			p.scheduleDiagnostics(uriOf(msg))
		case "textDocument/didClose":
			if uri := uriOf(msg); uri != "" {
				p.timersMu.Lock()
				if t, ok := p.timers[uri]; ok {
					t.Stop()
					delete(p.timers, uri)
				}
				p.timersMu.Unlock()
			}
		}
		if err := p.sendToServer(msg); err != nil {
			return err
		}
	}
}

// serverToClient forwards what comes from the server, absorbing what is ours.
func (p *proxy) serverToClient(out io.Reader) error {
	r := bufio.NewReaderSize(out, 1<<20)
	for {
		msg, body, err := readMessage(r)
		if err != nil {
			return err
		}
		if msg == nil {
			if err := writeFrame(&p.clientMu, p.toClient, body); err != nil {
				return err
			}
			continue
		}
		if p.handleDiagnosticResponse(msg) {
			continue
		}
		if p.answerServerRequest(msg) {
			continue
		}
		if err := p.sendToClient(msg); err != nil {
			return err
		}
	}
}

// runLSPProxy starts the given server and bridges it to the editor over stdio.
func runLSPProxy(server string, args []string) error {
	var debug io.Writer
	if path := os.Getenv("NANI_LSP_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			defer f.Close()
			debug = f
		}
	}

	cmd := exec.Command(server, args...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", server, err)
	}

	p := newProxy(stdin, os.Stdout, debug)
	p.logf("proxy started: %s %s", server, strings.Join(args, " "))

	errc := make(chan error, 2)
	go func() { errc <- p.clientToServer(os.Stdin) }()
	go func() { errc <- p.serverToClient(stdout) }()

	err = <-errc
	stdin.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err == io.EOF {
		return nil
	}
	return err
}
