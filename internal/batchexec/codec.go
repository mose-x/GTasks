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
	payload, err := stripEnvelope(body)
	if err != nil {
		return nil, err
	}

	var frames [][]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &frames); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
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
		if len(frame) > 3 && len(frame[3]) > 0 && string(frame[3]) != "null" {
			result.Error = frame[3]
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

// stripEnvelope removes the )]}' guard and any chunk-length framing.
func stripEnvelope(body string) (string, error) {
	current := strings.TrimSpace(body)
	for current != "" {
		if strings.HasPrefix(current, ")]}'") {
			newline := strings.IndexByte(current, '\n')
			if newline < 0 {
				return "", ErrMalformedResponse
			}
			current = strings.TrimSpace(current[newline+1:])
			continue
		}
		if current[0] == '[' {
			return current, nil
		}
		newline := strings.IndexByte(current, '\n')
		if newline > 0 && isChunkLength(strings.TrimSpace(current[:newline])) {
			current = strings.TrimSpace(current[newline+1:])
			continue
		}
		return "", fmt.Errorf("%w: 响应以 %q 开头", ErrMalformedResponse, truncate(current, 64))
	}
	return "", ErrMalformedResponse
}

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

func decodeIndex(raw json.RawMessage) (int, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
