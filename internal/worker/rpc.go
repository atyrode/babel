package worker

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// The native RPC frames this package reads and writes. The names are OMP's
// (packages/coding-agent/src/modes/rpc/rpc-types.ts at v18.1.11); nothing here
// is Babel's to define, which is why the set is small and stated once.
const (
	frameReady             = "ready"
	frameResponse          = "response"
	frameChunk             = "rpc_chunk"
	frameHostToolCall      = "host_tool_call"
	frameHostToolCancel    = "host_tool_cancel"
	frameHostURIRequest    = "host_uri_request"
	frameExtensionUI       = "extension_ui_request"
	frameAgentStart        = "agent_start"
	frameAgentEnd          = "agent_end"
	frameTurnStart         = "turn_start"
	frameTurnEnd           = "turn_end"
	frameToolStart         = "tool_execution_start"
	frameToolEnd           = "tool_execution_end"
	frameModelChanged      = "model_changed"
	frameCompactStart      = "auto_compaction_start"
	frameCompactEnd        = "auto_compaction_end"
	frameRetryStart        = "auto_retry_start"
	frameRetryEnd          = "auto_retry_end"
	frameRetryFallback     = "retry_fallback_applied"
	framePromptResult      = "prompt_result"
	frameExtensionErr      = "extension_error"
	frameMessageEnd        = "message_end"
	frameFallbackSucceeded = "retry_fallback_succeeded"

	commandNegotiate    = "negotiate_protocol"
	commandSetHostTools = "set_host_tools"
	commandPrompt       = "prompt"
	commandSessionStats = "get_session_stats"
	commandAbort        = "abort"

	// rpcProtocolVersion is the transport Babel negotiates: v2's chunked
	// framing is what makes a large tool result or a long final message
	// lossless rather than truncated by the engine's physical frame bound.
	rpcProtocolVersion = 2
)

// frame is one decoded stdout object. Fields belonging to other frame kinds
// stay zero; Raw keeps every top-level field so a frame Babel does not
// interpret is still visible to the caller that counts them.
type frame struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// response
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
	Code    string          `json:"code"`
	Data    json.RawMessage `json:"data"`

	// ready
	ProtocolVersion   int   `json:"protocolVersion"`
	SupportedVersions []int `json:"supportedProtocolVersions"`
	MaxFrameBytes     int   `json:"maxFrameBytes"`
	MaxReassembled    int   `json:"maxReassembledFrameBytes"`

	// host_tool_call / host_tool_cancel
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
	TargetID   string          `json:"targetId"`

	// agent_end, prompt_result
	IsTerminal   *bool           `json:"isTerminal"`
	AgentInvoked *bool           `json:"agentInvoked"`
	Messages     json.RawMessage `json:"messages"`

	// model_changed / retry_fallback_succeeded
	Model json.RawMessage `json:"model"`

	// retry_fallback_applied / retry_fallback_succeeded
	From string `json:"from"`
	To   string `json:"to"`
	Role string `json:"role"`

	// message_end: decode only accounting, never assistant content.
	Message *struct {
		Role string `json:"role"`
		AssistantMessageAccounting
	} `json:"message"`

	// extension_error
	ExtensionPath string `json:"extensionPath"`
	Event         string `json:"event"`

	// host_uri_request
	Operation string `json:"operation"`
	URL       string `json:"url"`
}

// chunk is one rpc_chunk frame of a v2 logical frame.
type chunk struct {
	ChunkID    string `json:"chunkId"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	ByteLength int    `json:"byteLength"`
	Data       string `json:"data"`
}

// frameReader reads native frames off the engine's stdout: one JSON object
// per line, with v2 chunk sequences reassembled into the logical frame they
// carry. It is a faithful reimplementation of the engine's own
// RpcFrameDecoder rules — chunk id, index order, count, byte length and the
// reassembly ceiling are all validated, and an interrupted or interleaved
// sequence is a decode failure — because the alternative was a Go client
// library upstream does not ship.
type frameReader struct {
	reader         *bufio.Reader
	maxFrame       int
	maxReassembled int

	// pending is the chunk sequence in flight, if any.
	pending *pendingChunks
}

type pendingChunks struct {
	id     string
	count  int
	length int
	next   int
	buf    []byte
}

func newFrameReader(r io.Reader, maxFrame, maxReassembled int) *frameReader {
	return &frameReader{
		reader:         bufio.NewReaderSize(r, readBufferSize),
		maxFrame:       maxFrame,
		maxReassembled: maxReassembled,
	}
}

// next returns the next logical frame. It returns io.EOF at end of stream,
// ErrOversizedFrame on a physical or reassembled bound, and ErrMalformedFrame
// on anything that is not one JSON object or a well-formed chunk sequence.
// Every stop is final: the reader does not resynchronize, because a stream
// that has lost its framing cannot be trusted to have kept its content.
func (fr *frameReader) next() (frame, []byte, error) {
	for {
		line, err := readLine(fr.reader, fr.maxFrame)
		if err != nil {
			if errors.Is(err, io.EOF) && len(bytes.TrimSpace(line)) == 0 {
				return frame{}, nil, io.EOF
			}
			if !errors.Is(err, io.EOF) {
				return frame{}, nil, err
			}
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return frame{}, nil, fmt.Errorf("%w: %v", ErrMalformedFrame, err)
		}
		if probe.Type == frameChunk {
			whole, done, err := fr.reassemble(line)
			if err != nil {
				return frame{}, nil, err
			}
			if !done {
				continue
			}
			line = whole
		} else if fr.pending != nil {
			return frame{}, nil, fmt.Errorf("%w: chunk sequence %q interrupted by a %s frame",
				ErrMalformedFrame, fr.pending.id, probe.Type)
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			return frame{}, nil, fmt.Errorf("%w: %v", ErrMalformedFrame, err)
		}
		return f, line, nil
	}
}

// reassemble folds one chunk into the pending sequence and reports the whole
// frame once the last chunk lands.
func (fr *frameReader) reassemble(line []byte) ([]byte, bool, error) {
	var c chunk
	if err := json.Unmarshal(line, &c); err != nil {
		return nil, false, fmt.Errorf("%w: chunk: %v", ErrMalformedFrame, err)
	}
	if c.ChunkID == "" || c.Count <= 0 || c.Index < 0 || c.Index >= c.Count || c.ByteLength <= 0 {
		return nil, false, fmt.Errorf("%w: chunk %q index %d of %d (%d bytes) is not well formed",
			ErrMalformedFrame, c.ChunkID, c.Index, c.Count, c.ByteLength)
	}
	if c.ByteLength > fr.maxReassembled {
		return nil, false, fmt.Errorf("%w: chunk sequence %q declares %d bytes over a %d byte reassembly limit",
			ErrOversizedFrame, c.ChunkID, c.ByteLength, fr.maxReassembled)
	}
	p := fr.pending
	switch {
	case p == nil:
		if c.Index != 0 {
			return nil, false, fmt.Errorf("%w: chunk sequence %q starts at index %d", ErrMalformedFrame, c.ChunkID, c.Index)
		}
		p = &pendingChunks{id: c.ChunkID, count: c.Count, length: c.ByteLength, buf: make([]byte, 0, c.ByteLength)}
		fr.pending = p
	case p.id != c.ChunkID:
		return nil, false, fmt.Errorf("%w: chunk sequence %q interleaved with %q", ErrMalformedFrame, p.id, c.ChunkID)
	case p.count != c.Count || p.length != c.ByteLength:
		return nil, false, fmt.Errorf("%w: chunk sequence %q changed its count or length mid-stream", ErrMalformedFrame, p.id)
	case p.next != c.Index:
		return nil, false, fmt.Errorf("%w: chunk sequence %q expected index %d, got %d", ErrMalformedFrame, p.id, p.next, c.Index)
	}
	segment, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil {
		fr.pending = nil
		return nil, false, fmt.Errorf("%w: chunk sequence %q carries invalid base64", ErrMalformedFrame, p.id)
	}
	if len(p.buf)+len(segment) > p.length {
		fr.pending = nil
		return nil, false, fmt.Errorf("%w: chunk sequence %q exceeds its declared %d bytes", ErrMalformedFrame, p.id, p.length)
	}
	p.buf = append(p.buf, segment...)
	p.next++
	if p.next < p.count {
		return nil, false, nil
	}
	fr.pending = nil
	if len(p.buf) != p.length {
		return nil, false, fmt.Errorf("%w: chunk sequence %q reassembled %d bytes, declared %d",
			ErrMalformedFrame, p.id, len(p.buf), p.length)
	}
	if !utf8.Valid(p.buf) {
		return nil, false, fmt.Errorf("%w: chunk sequence %q is not UTF-8", ErrMalformedFrame, p.id)
	}
	return p.buf, true, nil
}

// readLine reads one newline-terminated line, enforcing max on the payload.
// It exists instead of bufio.Scanner because Scanner only reports ErrTooLong
// once a token exceeds its *buffer*, so a small configured maximum would not
// be enforced at all.
func readLine(reader *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			line = append(line, chunk...)
			if len(line) > max {
				return nil, fmt.Errorf("%w: over %d bytes", ErrOversizedFrame, max)
			}
			continue
		}
		line = append(line, chunk...)
		if err != nil {
			return line, err
		}
		if len(line)-1 > max {
			return nil, fmt.Errorf("%w: %d bytes over a %d byte limit", ErrOversizedFrame, len(line)-1, max)
		}
		return line[:len(line)-1], nil
	}
}

// Commands Babel writes. Each is one JSON object per line; inbound commands
// are never chunked, and every one Babel sends is small.
type negotiateCommand struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocolVersion"`
}

type setHostToolsCommand struct {
	ID    string           `json:"id"`
	Type  string           `json:"type"`
	Tools []hostToolOnWire `json:"tools"`
}

// hostToolOnWire is OMP's RpcHostToolDefinition.
type hostToolOnWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	LoadMode    string          `json:"loadMode,omitempty"`
}

type promptCommand struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

type plainCommand struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// hostToolResult answers one host_tool_call. Content is what the model reads;
// IsError makes it a tool error the model is expected to correct for.
type hostToolResult struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Result  hostToolPayload `json:"result"`
	IsError bool            `json:"isError,omitempty"`
}

type hostToolPayload struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textResult(id, text string, isError bool) hostToolResult {
	return hostToolResult{
		Type:    "host_tool_result",
		ID:      id,
		Result:  hostToolPayload{Content: []contentBlock{{Type: "text", Text: text}}},
		IsError: isError,
	}
}

// extensionUIResponse cancels an extension's dialog. Babel has no operator at
// the other end of a supervised run, and an unanswered dialog would stall the
// engine until its own timeout.
type extensionUIResponse struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Cancelled bool   `json:"cancelled"`
}

// hostURIResult refuses a host URI read or write: Babel registers no schemes,
// so any such request is the engine asking for something nobody offered.
type hostURIResult struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	IsError bool   `json:"isError"`
	Error   string `json:"error"`
}

// commandIDs mints correlation ids for Babel's commands.
type commandIDs struct{ n int }

func (c *commandIDs) next() string {
	c.n++
	return "babel-" + strconv.Itoa(c.n)
}

// sessionStats is the part of get_session_stats Babel records.
type sessionStats struct {
	Tokens struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		Reasoning  int64 `json:"reasoning"`
		CacheRead  int64 `json:"cacheRead"`
		CacheWrite int64 `json:"cacheWrite"`
		Total      int64 `json:"total"`
	} `json:"tokens"`
	Cost              float64 `json:"cost"`
	ToolCalls         int     `json:"toolCalls"`
	AssistantMessages int     `json:"assistantMessages"`
}

// usageOf reads a get_session_stats payload into the receipt's Usage.
func usageOf(data json.RawMessage) (*Usage, error) {
	var stats sessionStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return nil, err
	}
	return &Usage{
		InputTokens:      stats.Tokens.Input,
		OutputTokens:     stats.Tokens.Output,
		ReasoningTokens:  stats.Tokens.Reasoning,
		CacheReadTokens:  stats.Tokens.CacheRead,
		CacheWriteTokens: stats.Tokens.CacheWrite,
		TotalTokens:      stats.Tokens.Total,
		Cost:             stats.Cost,
		ToolCalls:        stats.ToolCalls,
		Messages:         stats.AssistantMessages,
	}, nil
}
