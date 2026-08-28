// Package transcript presents harness session logs as a common stream of events.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	rawTextLimit  = 2000
	maxRecordSize = 4 << 20
)

// Event is one line of a session transcript in display order. Time is nil when
// the source record has no valid RFC3339 timestamp.
type Event struct {
	Index int     `json:"index"`
	Role  string  `json:"role"`
	Kind  string  `json:"kind"`
	Time  *string `json:"time"`
	Text  string  `json:"text"`
}

// Events reads a primary session log and returns the requested event window.
// Malformed and unrecognized records are retained as raw events; only opening
// or reading the file can fail.
func Events(path, harness string, offset, limit int) (total int, events []Event, err error) {
	if offset < 0 {
		return 0, nil, fmt.Errorf("offset must not be negative")
	}
	if limit < 0 {
		return 0, nil, fmt.Errorf("limit must not be negative")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		line, oversized, present, readErr := readRecordLine(reader)
		if present {
			event, ok := Event{}, false
			if !oversized {
				event, ok = parse(line, harness)
			}
			if !ok {
				event = rawEvent(line)
			}
			event.Index = total
			if total >= offset && len(events) < limit {
				events = append(events, event)
			}
			total++
		}
		switch readErr {
		case nil:
			continue
		case io.EOF:
			return total, events, nil
		default:
			return total, events, readErr
		}
	}
}

// readRecordLine retains a bounded prefix while draining one logical line.
// Oversized records become raw events instead of allowing an untrusted log to
// grow the process without bound.
func readRecordLine(reader *bufio.Reader) (line []byte, oversized, present bool, err error) {
	chunk, err := reader.ReadSlice('\n')
	present = len(chunk) != 0
	if err != bufio.ErrBufferFull {
		return trimLine(chunk), false, present, err
	}

	line = append(line, chunk...)
	for err == bufio.ErrBufferFull {
		chunk, err = reader.ReadSlice('\n')
		room := maxRecordSize - len(line)
		if room > len(chunk) {
			room = len(chunk)
		}
		if room > 0 {
			line = append(line, chunk[:room]...)
		}
		if room < len(chunk) {
			oversized = true
		}
	}
	return trimLine(line), oversized, present, err
}

func trimLine(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	return bytes.TrimSuffix(line, []byte{'\r'})
}

func parse(line []byte, harness string) (Event, bool) {
	switch harness {
	case "omp":
		return parseOMP(line)
	case "codex":
		return parseCodex(line)
	case "claude":
		return parseClaude(line)
	default:
		return Event{}, false
	}
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func parseOMP(line []byte) (Event, bool) {
	var record struct {
		Type      string  `json:"type"`
		Timestamp string  `json:"timestamp"`
		Message   message `json:"message"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "message" {
		return Event{}, false
	}
	text, ok := messageText(record.Message.Content, "text")
	if !ok || record.Message.Role == "" {
		return Event{}, false
	}
	return Event{Role: record.Message.Role, Kind: "message", Time: eventTime(record.Timestamp), Text: text}, true
}

func parseCodex(line []byte) (Event, bool) {
	var record struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "response_item" || record.Payload.Type != "message" || record.Payload.Role == "" {
		return Event{}, false
	}
	text, ok := messageText(record.Payload.Content, "input_text", "output_text", "text")
	if !ok {
		return Event{}, false
	}
	return Event{Role: record.Payload.Role, Kind: "message", Time: eventTime(record.Timestamp), Text: text}, true
}

func parseClaude(line []byte) (Event, bool) {
	var record struct {
		Type      string  `json:"type"`
		Timestamp string  `json:"timestamp"`
		Message   message `json:"message"`
	}
	if json.Unmarshal(line, &record) != nil || (record.Type != "user" && record.Type != "assistant") {
		return Event{}, false
	}
	role := record.Message.Role
	if role == "" {
		role = record.Type
	}
	text, ok := messageText(record.Message.Content, "text")
	if !ok {
		return Event{}, false
	}
	return Event{Role: role, Kind: "message", Time: eventTime(record.Timestamp), Text: text}, true
}

func messageText(raw json.RawMessage, acceptedTypes ...string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct, true
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	accepted := make(map[string]struct{}, len(acceptedTypes))
	for _, typ := range acceptedTypes {
		accepted[typ] = struct{}{}
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if _, ok := accepted[part.Type]; ok {
			texts = append(texts, part.Text)
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n"), true
}

func eventTime(value string) *string {
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return nil
	}
	return &value
}

func rawEvent(line []byte) Event {
	if utf8.Valid(line) {
		runes := 0
		for i := 0; i < len(line); {
			if runes == rawTextLimit {
				return Event{Kind: "raw", Text: string(line[:i])}
			}
			_, size := utf8.DecodeRune(line[i:])
			i += size
			runes++
		}
		return Event{Kind: "raw", Text: string(line)}
	}

	var text strings.Builder
	text.Grow(min(len(line), rawTextLimit))
	for i, runes := 0, 0; i < len(line) && runes < rawTextLimit; runes++ {
		r, size := utf8.DecodeRune(line[i:])
		text.WriteRune(r)
		i += size
	}
	return Event{Kind: "raw", Text: text.String()}
}
