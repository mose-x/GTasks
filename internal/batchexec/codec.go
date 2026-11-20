// Package batchexec speaks Google's internal batchexecute RPC protocol.
//
// The wire format is documented by community research (see pybatchexecute and
// "Deciphering Google's mysterious batchexecute system"). Requests carry a
// single form field f.req whose value is a doubly encoded array of positional
// RPC calls; responses are prefixed with the )]}' guard and, when rt=c is
// requested, framed with hexadecimal chunk lengths.
package batchexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrMalformedResponse is returned when a body cannot be parsed as batchexecute.
var ErrMalformedResponse = errors.New("batchexec: 无法解析响应")

// Call is one RPC inside a batch.
type Call struct {
	RpcID string
	// Args is encoded to JSON and placed in the positional slot Google expects.
	Args any
}

// Response is one decoded RPC result.
type Response struct {
	RpcID string          `json:"rpcid"`
	Index int             `json:"index"`
	Data  json.RawMessage `json:"data,omitempty"`
	// Error is populated when Google returned an error frame for this RPC.
	Error json.RawMessage `json:"error,omitempty"`
	// Raw keeps the undecoded inner payload for inspection.
	Raw string `json:"raw,omitempty"`
}

// Encode builds the f.req form value for a batch of calls.
func Encode(calls []Call) (string, error) {
	inner := make([]any, 0, len(calls))
	for position, call := range calls {
		args := call.Args
		if args == nil {
			args = []any{}
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("batchexec: 编码 %s 参数失败: %w", call.RpcID, err)
		}
		inner = append(inner, []any{call.RpcID, string(encoded), nil, strconv.Itoa(position + 1)})
	}
	outer, err := json.Marshal([]any{inner})
	if err != nil {
		return "", fmt.Errorf("batchexec: 编码批次失败: %w", err)
	}
	return string(outer), nil
}

// Decode parses a batchexecute response body into per-RPC results. Non-RPC
// frames such as "di", "af.httprm" and "e" are skipped.
func Decode(body string) ([]Response, error) {
	frames, err := splitFrames(body)
	if err != nil {
		return nil, err
	}

	results := make([]Response, 0, len(frames))
	for _, frame := range frames {
		if len(frame) < 3 {
			continue
		}
		var tag string
		if err := json.Unmarshal(frame[0], &tag); err != nil || tag != "wrb.fr" {
			continue
		}

		var rpcID string
		if err := json.Unmarshal(frame[1], &rpcID); err != nil {
			continue
		}

		result := Response{RpcID: rpcID, Index: len(results) + 1}
		if index, err := decodeIndex(frame[len(frame)-1]); err == nil {
			result.Index = index
		}
		// Google keeps the error payload in different slots depending on how
		// the call failed: a JSON string for transport-level refusals, a
		// structured array for service errors. The trailing slot is the
		// sequence marker, so it is left out of the search.
		if err := errorSlot(frame[3 : len(frame)-1]); err != nil {
			result.Error = err
		}

		// frame[2] is a JSON string holding the real payload.
		var inner string
		if err := json.Unmarshal(frame[2], &inner); err != nil {
			result.Raw = string(frame[2])
			results = append(results, result)
			continue
		}
		result.Raw = inner
		if json.Valid([]byte(inner)) {
			result.Data = json.RawMessage(inner)
		}
		results = append(results, result)
	}
	return results, nil
}

// envelopeGuard is the XSSI prefix every batchexecute body starts with.
const envelopeGuard = ")]}'"

// splitFrames walks a response body and returns every frame it carries. With
// rt=c Google frames the payload as a guard line followed by length-prefixed
// chunks, and a single response routinely holds more than one chunk, so all of
// them are collected instead of only the first.
func splitFrames(body string) ([][]json.RawMessage, error) {
	var frames [][]json.RawMessage
	chunks := 0

	rest := body
	for {
		rest = strings.TrimLeft(rest, " \t\r\n")
		if rest == "" {
			break
		}
		if strings.HasPrefix(rest, envelopeGuard) {
			rest = rest[len(envelopeGuard):]
			continue
		}
		if rest[0] != '[' {
			marker, remainder := splitLine(rest)
			if !isChunkLength(marker) {
				return nil, fmt.Errorf("%w: 响应以 %q 开头", ErrMalformedResponse, truncate(strings.TrimSpace(rest), 64))
			}
			rest = remainder
			continue
		}
		chunk, consumed, err := decodeChunk(rest)
		if err != nil {
			return nil, err
		}
		frames = append(frames, chunk...)
		chunks++
		rest = rest[consumed:]
	}

	if chunks == 0 {
		return nil, ErrMalformedResponse
	}
	return frames, nil
}

// decodeChunk reads the single JSON value at the start of text and reports how
// many bytes it occupied, so the caller can continue with the next chunk.
func decodeChunk(text string) ([][]json.RawMessage, int, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}
	frames, err := asFrames(value)
	if err != nil {
		return nil, 0, err
	}
	return frames, int(decoder.InputOffset()), nil
}

// asFrames accepts both the usual list of frames and a single bare frame, so an
// unexpected wrapper does not throw the payload away.
func asFrames(value json.RawMessage) ([][]json.RawMessage, error) {
	var frames [][]json.RawMessage
	if err := json.Unmarshal(value, &frames); err == nil {
		return frames, nil
	}
	var frame []json.RawMessage
	if err := json.Unmarshal(value, &frame); err != nil {
		return nil, fmt.Errorf("%w: 数据块不是帧数组", ErrMalformedResponse)
	}
	return [][]json.RawMessage{frame}, nil
}

// errorSlot returns the first populated error slot, or nil when the call
// carried no error.
func errorSlot(slots []json.RawMessage) json.RawMessage {
	for _, slot := range slots {
		if len(slot) == 0 || string(slot) == "null" {
			continue
		}
		return slot
	}
	return nil
}

// isChunkLength reports whether candidate looks like a chunk-length marker.
// Google writes them in decimal, but hex has been observed too, so both count.
func isChunkLength(candidate string) bool {
	if candidate == "" || len(candidate) > 10 {
		return false
	}
	for _, char := range candidate {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func splitLine(text string) (line, remainder string) {
	if newline := strings.IndexByte(text, '\n'); newline >= 0 {
		return strings.TrimSpace(text[:newline]), text[newline+1:]
	}
	return strings.TrimSpace(text), ""
}

// decodeIndex reads the trailing sequence slot, which Google sends either as a
// number or as a numeric string.
func decodeIndex(raw json.RawMessage) (int, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.Atoi(text)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
