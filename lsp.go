package main

// Proxy LSP entre um cliente que só entende diagnósticos push (o plugin lsp do
// micro) e um servidor que os entrega por pull (o tsgo, TypeScript nativo em Go).
//
// O que ele faz, além de repassar as mensagens:
//   - anuncia ao servidor as capabilities de pull que o cliente não anuncia;
//   - responde sozinho aos requests do servidor (workspace/configuration e
//     client/registerCapability), que bloqueiam o tsgo até receberem resposta;
//   - a cada didOpen/didChange/didSave pede textDocument/diagnostic e converte
//     a resposta em textDocument/publishDiagnostics para o cliente.

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

// Prefixo dos ids criados aqui. Os ids do cliente são números, então uma string
// com prefixo nunca colide com eles.
const diagIDPrefix = "ff-diag-"

// debounce entre a última edição e o pedido de diagnósticos.
const diagDebounce = 300 * time.Millisecond

type message map[string]any

type proxy struct {
	toServer  *bufio.Writer
	toClient  *bufio.Writer
	serverMu  sync.Mutex
	clientMu  sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]string // id do pedido de diagnóstico -> uri
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

// readMessage lê um frame LSP: cabeçalhos, linha em branco, corpo de N bytes.
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
					return nil, nil, fmt.Errorf("Content-Length inválido: %w", err)
				}
			}
		}
	}
	if length <= 0 {
		return nil, nil, fmt.Errorf("frame sem Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, nil, err
	}
	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		// Repassa o corpo cru: não entender uma mensagem não é motivo para
		// derrubar a conexão.
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

// addPullCapabilities declara ao servidor o suporte a pull diagnostics e a
// workspace/configuration, que o proxy responde no lugar do cliente.
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

// scheduleDiagnostics pede diagnósticos da uri após o debounce, reiniciando o
// contador a cada nova edição do mesmo arquivo.
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

	p.logf("-> pedindo diagnósticos de %s (id %s)", uri, id)
	err := p.sendToServer(message{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "textDocument/diagnostic",
		"params":  map[string]any{"textDocument": map[string]any{"uri": uri}},
	})
	if err != nil {
		p.logf("falha ao pedir diagnósticos: %v", err)
	}
}

// handleDiagnosticResponse converte a resposta do pull em uma notificação push.
// Retorna false quando a mensagem não é uma resposta nossa.
func (p *proxy) handleDiagnosticResponse(msg message) bool {
	id, ok := msg["id"].(string)
	if !ok || !strings.HasPrefix(id, diagIDPrefix) {
		return false
	}
	p.pendingMu.Lock()
	uri := p.pending[id]
	delete(p.pending, id)
	p.pendingMu.Unlock()

	// Servidor sem suporte a pull responde erro. Publicar uma lista vazia aqui
	// apagaria os diagnósticos que ele já mandou por push.
	if e, ok := msg["error"]; ok {
		p.logf("servidor recusou o pull de diagnósticos (%v); mantendo os do push", e)
		return true
	}

	items := []any{}
	if result, ok := msg["result"].(map[string]any); ok {
		if got, ok := result["items"].([]any); ok {
			items = got
		}
	}
	p.logf("<- %d diagnóstico(s) para %s", len(items), uri)
	err := p.sendToClient(message{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params":  map[string]any{"uri": uri, "diagnostics": items},
	})
	if err != nil {
		p.logf("falha ao publicar diagnósticos: %v", err)
	}
	return true
}

// answerServerRequest responde aos requests que o cliente não sabe responder.
// O tsgo fica bloqueado até recebê-los.
func (p *proxy) answerServerRequest(msg message) bool {
	method, _ := msg["method"].(string)
	if _, hasID := msg["id"]; !hasID {
		return false
	}
	var result any
	switch method {
	case "workspace/configuration":
		// Um objeto vazio por item pedido: o tsgo usa os defaults.
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
	p.logf("respondendo request do servidor: %s", method)
	if err := p.sendToServer(message{"jsonrpc": "2.0", "id": msg["id"], "result": result}); err != nil {
		p.logf("falha ao responder %s: %v", method, err)
	}
	return true
}

// clientToServer repassa o que vem do editor, observando as edições.
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
			p.logf("initialize do cliente, capabilities de pull injetadas")
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

// serverToClient repassa o que vem do servidor, absorvendo o que é nosso.
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

// runLSPProxy sobe o servidor informado e faz a ponte com o editor via stdio.
func runLSPProxy(server string, args []string) error {
	var debug io.Writer
	if path := os.Getenv("FF_LSP_LOG"); path != "" {
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
		return fmt.Errorf("não consegui iniciar %s: %w", server, err)
	}

	p := newProxy(stdin, os.Stdout, debug)
	p.logf("proxy iniciado: %s %s", server, strings.Join(args, " "))

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
