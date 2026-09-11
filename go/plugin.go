// Package streamcore is the Go SDK for StreamCoreAI plugins.
//
// A plugin is a process the server starts and talks to over stdio in JSON-RPC.
// Write a handler, call Run, and point a plugin.yaml at the binary:
//
//	plugin := streamcore.New()
//	plugin.OnExecute(func(call streamcore.Call) (any, error) {
//	    var args struct{ Location string `json:"location"` }
//	    if err := call.Bind(&args); err != nil {
//	        return nil, err
//	    }
//	    return "It is sunny in " + args.Location, nil
//	})
//	plugin.Run()
//
// Return a string to have the agent say it. Return a Result to also push
// packets at the caller's device, which is how a plugin drives hardware without
// anything being added to the server.
package streamcore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
)

// Protocol is the version this SDK speaks. The server announces its own at
// initialize; a mismatch is not fatal, since both sides ignore what they do not
// recognise.
const Protocol = 2

// Call is one tool invocation.
type Call struct {
	// Tool is which of the plugin's tools was called. A single-tool plugin can
	// ignore it.
	Tool string

	// SessionID is the conversation the call belongs to. Use it to keep two
	// callers' state apart, and to address anything you emit later.
	SessionID string

	// Params is the raw argument object. Bind is usually easier.
	Params json.RawMessage
}

// Bind unmarshals the call's arguments into v.
func (c Call) Bind(v any) error {
	if len(c.Params) == 0 {
		return nil
	}
	return json.Unmarshal(c.Params, v)
}

// Event is a server lifecycle event, delivered to plugins whose manifest
// subscribes to it with `events:`.
type Event struct {
	Type      string
	SessionID string
	TurnID    string
	TurnSeq   uint64
	Data      json.RawMessage
}

// Bind unmarshals the event payload into v.
func (e Event) Bind(v any) error {
	if len(e.Data) == 0 {
		return nil
	}
	return json.Unmarshal(e.Data, v)
}

// Init is the handshake. Config is the plugin's own table from the server's
// config.toml, so credentials live in the operator's config rather than in a
// dotenv beside the plugin.
type Init struct {
	Plugin   string
	Protocol int
	Config   json.RawMessage
}

// Bind unmarshals the plugin's configuration into v.
func (i Init) Bind(v any) error {
	if len(i.Config) == 0 {
		return nil
	}
	return json.Unmarshal(i.Config, v)
}

// Ready arrives once every plugin has started, listing the tools the server
// ended up with. It is the moment a plugin can decide what to offer knowing
// which peers exist.
type Ready struct {
	Tools []string
}

// Has reports whether a tool is loaded, which is how a plugin decides to offer
// something that only makes sense alongside another plugin.
func (r Ready) Has(name string) bool {
	for _, tool := range r.Tools {
		if tool == name {
			return true
		}
	}
	return false
}

// Result is a richer answer than a string: words for the agent to say, and
// packets for the caller's device.
type Result struct {
	Speak string     `json:"speak,omitempty"`
	Emit  []Emission `json:"emit,omitempty"`
}

// Emission is one topic-addressed packet bound for the client.
type Emission struct {
	Topic     string `json:"topic"`
	Payload   any    `json:"payload,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// Tool describes a callable tool. Returning tools from OnInitialize declares
// them at startup, which is how a plugin whose surface depends on its
// configuration — or on what else the server loaded — advertises itself.
type Tool struct {
	Name                 string `json:"name"`
	Description          string `json:"description"`
	Parameters           any    `json:"parameters,omitempty"`
	ConfirmationRequired bool   `json:"confirmation_required,omitempty"`

	// Internal keeps a tool out of the model's view while leaving it callable
	// by other plugins — a join between two plugins that the model has no
	// business invoking.
	Internal bool `json:"internal,omitempty"`

	ConfirmationPrompt string `json:"confirmation_prompt,omitempty"`
	ThinkingSound      bool   `json:"thinking_sound,omitempty"`
}

// Plugin is one plugin process.
type Plugin struct {
	in  io.Reader
	out io.Writer
	log io.Writer

	writeMu sync.Mutex
	encoder *json.Encoder

	onExecute    func(Call) (any, error)
	onEvent      func(Event) (any, error)
	onInitialize func(Init) ([]Tool, error)
	onConfirm    func(Call) (string, error)
	onReady      func(Ready) ([]Tool, error)

	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan rpcMessage
}

// New returns a plugin wired to stdin and stdout, logging to stderr.
func New() *Plugin {
	return &Plugin{
		in:      os.Stdin,
		out:     os.Stdout,
		log:     os.Stderr,
		pending: make(map[string]chan rpcMessage),
	}
}

// OnExecute registers the tool handler. Return a string for the agent to say,
// or a Result to also emit packets.
func (p *Plugin) OnExecute(handler func(Call) (any, error)) { p.onExecute = handler }

// OnEvent registers the lifecycle handler, for a manifest that declares
// `events:`. Return nil, a string, or a Result.
func (p *Plugin) OnEvent(handler func(Event) (any, error)) { p.onEvent = handler }

// OnInitialize registers the startup handler. Returning tools declares them;
// returning nil keeps whatever the manifest listed.
func (p *Plugin) OnInitialize(handler func(Init) ([]Tool, error)) { p.onInitialize = handler }

// OnReady registers the second declaration pass. Returning tools replaces what
// the plugin declared at initialize.
//
// It exists because a plugin cannot answer "what do I offer" in isolation when
// the answer depends on another plugin being loaded. At initialize nothing else
// may have started yet; by the time this fires, everything has.
func (p *Plugin) OnReady(handler func(Ready) ([]Tool, error)) { p.onReady = handler }

// OnConfirm registers the handler that describes a pending action in the words
// the agent should say out loud, for a tool that requires confirmation.
//
// A manifest can carry a `confirmation_prompt` template instead, which needs no
// handler. This is for prompts that are not plain substitution — reading an
// instruction back inside a sentence, say.
func (p *Plugin) OnConfirm(handler func(Call) (string, error)) { p.onConfirm = handler }

// Logf writes to stderr, which the server folds into its own log.
func (p *Plugin) Logf(format string, args ...any) {
	fmt.Fprintf(p.log, format+"\n", args...)
}

// Emit pushes a packet at a conversation's client without having been asked
// for one — a sensor reading, a timer firing, progress on a long job.
func (p *Plugin) Emit(sessionID, topic string, payload any) error {
	if sessionID == "" {
		return errors.New("emit: session_id is required")
	}
	_, err := p.request("emit", Emission{Topic: topic, Payload: payload, SessionID: sessionID})
	return err
}

// CallTool runs another plugin's tool. The server routes it, so neither plugin
// has to know where the other lives or that it exists at build time.
func (p *Plugin) CallTool(sessionID, name string, arguments any) (string, error) {
	raw, err := p.request("tools/call", map[string]any{
		"name":       name,
		"arguments":  arguments,
		"session_id": sessionID,
	})
	if err != nil {
		return "", err
	}
	return decodeString(raw), nil
}

// Complete asks the server's configured model for a one-shot completion, so a
// plugin that needs a model does not carry a second API key.
func (p *Plugin) Complete(sessionID, system, prompt string) (string, error) {
	raw, err := p.request("llm/complete", map[string]any{
		"system":     system,
		"prompt":     prompt,
		"session_id": sessionID,
	})
	if err != nil {
		return "", err
	}
	return decodeString(raw), nil
}

// Search queries the deployment's knowledge base, so a plugin can ground an
// answer in the same corpus the agent uses rather than standing up a second
// retrieval stack beside it. A limit of zero uses the server's default.
func (p *Plugin) Search(sessionID, query string, limit int) ([]string, error) {
	raw, err := p.request("rag/search", map[string]any{
		"query":      query,
		"limit":      limit,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, err
	}
	var chunks []string
	if err := json.Unmarshal(raw, &chunks); err != nil {
		return nil, err
	}
	return chunks, nil
}

// Run reads requests until stdin closes. It must be the last call.
func (p *Plugin) Run() error {
	p.encoder = json.NewEncoder(p.out)

	scanner := bufio.NewScanner(p.in)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		message := make([]byte, len(line))
		copy(message, line)

		var envelope rpcMessage
		if err := json.Unmarshal(message, &envelope); err != nil {
			p.Logf("unparseable input: %s", message)
			continue
		}

		// No method means this is a reply to something we asked for.
		if envelope.Method == "" {
			p.deliver(envelope)
			continue
		}
		// Each request runs on its own goroutine so a slow tool does not block
		// the ones behind it, and so a handler may call back into the server
		// while still holding the call it is answering.
		go p.dispatch(envelope)
	}
	return scanner.Err()
}

func (p *Plugin) dispatch(req rpcMessage) {
	result, err := p.handle(req)
	if len(req.ID) == 0 {
		return
	}
	reply := rpcMessage{JSONRPC: "2.0", ID: req.ID}
	if err != nil {
		reply.Error = &rpcError{Code: -32000, Message: err.Error()}
	} else {
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			reply.Error = &rpcError{Code: -32603, Message: marshalErr.Error()}
		} else {
			reply.Result = encoded
		}
	}
	if err := p.write(reply); err != nil {
		p.Logf("reply failed: %v", err)
	}
}

func (p *Plugin) handle(req rpcMessage) (any, error) {
	switch req.Method {
	case "initialize":
		var params struct {
			Plugin   string          `json:"plugin"`
			Protocol int             `json:"protocol"`
			Config   json.RawMessage `json:"config"`
		}
		json.Unmarshal(req.Params, &params)
		if p.onInitialize == nil {
			return "initialized", nil
		}
		tools, err := p.onInitialize(Init(params))
		if err != nil {
			return nil, err
		}
		if len(tools) == 0 {
			return "initialized", nil
		}
		return map[string]any{"tools": tools}, nil

	case "execute":
		if p.onExecute == nil {
			return nil, errors.New("no execute handler registered")
		}
		return p.onExecute(Call{Tool: req.Tool, SessionID: req.SessionID, Params: req.Params})

	case "ready":
		if p.onReady == nil {
			return "ok", nil
		}
		var params struct {
			Tools []string `json:"tools"`
		}
		json.Unmarshal(req.Params, &params)
		tools, err := p.onReady(Ready(params))
		if err != nil {
			return nil, err
		}
		if len(tools) == 0 {
			return "ok", nil
		}
		return map[string]any{"tools": tools}, nil

	case "confirm":
		if p.onConfirm == nil {
			return "", nil
		}
		return p.onConfirm(Call{Tool: req.Tool, SessionID: req.SessionID, Params: req.Params})

	case "event":
		if p.onEvent == nil {
			return nil, nil
		}
		var params struct {
			Type      string          `json:"type"`
			SessionID string          `json:"session_id"`
			TurnID    string          `json:"turn_id"`
			TurnSeq   uint64          `json:"turn_seq"`
			Data      json.RawMessage `json:"data"`
		}
		json.Unmarshal(req.Params, &params)
		return p.onEvent(Event(params))

	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}

// request sends a call to the server and waits for its reply.
func (p *Plugin) request(method string, params any) (json.RawMessage, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := "sdk-" + strconv.FormatInt(p.nextID.Add(1), 10)
	rawID, _ := json.Marshal(id)

	reply := make(chan rpcMessage, 1)
	p.pendingMu.Lock()
	p.pending[id] = reply
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
	}()

	if err := p.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: encoded, ID: rawID}); err != nil {
		return nil, err
	}

	response := <-reply
	if response.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, response.Error.Message)
	}
	return response.Result, nil
}

func (p *Plugin) deliver(response rpcMessage) {
	var id string
	if err := json.Unmarshal(response.ID, &id); err != nil {
		p.Logf("reply with unreadable id: %s", response.ID)
		return
	}
	p.pendingMu.Lock()
	reply, ok := p.pending[id]
	p.pendingMu.Unlock()
	if !ok {
		p.Logf("reply to unknown request %s", id)
		return
	}
	reply <- response
}

func (p *Plugin) write(message rpcMessage) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.encoder.Encode(message)
}

// rpcMessage is every shape on the wire. Requests carry Method; replies carry
// Result or Error. ID is raw because each side numbers its own however it likes.
type rpcMessage struct {
	JSONRPC   string          `json:"jsonrpc"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *rpcError       `json:"error,omitempty"`
	ID        json.RawMessage `json:"id,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// decodeString unwraps a JSON string result, leaving anything else as raw JSON.
func decodeString(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}
