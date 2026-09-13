package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// ErrUnsupportedInput marks payloads the strict pipeline refuses to audit
// (FORMAT-003, §6.1, §21): the request is rejected, never "assumed safe".
var ErrUnsupportedInput = errors.New("audit input unsupported")

const (
	maxJSONDepth   = 64
	maxFieldCount  = 20000
	maxStringValue = 8 << 20 // per-string guard; overall body limit enforced at gateway
)

// ExtractResponses builds the AuditDocument from a /v1/responses request body
// (§21). Every accessible, parseable text surface is visited — input strings,
// message content parts, instructions, tool descriptions, function_call
// arguments and function_call_output. previous_response_id and other
// server-side-history references make the document unsupported in strict mode
// because the gateway cannot reconstruct the audited context (§6.1).
func ExtractResponses(body []byte, strict bool) (*AuditDocument, error) {
	doc := &AuditDocument{Coverage: CoverageFull}

	// Structural sanity before semantic extraction (FORMAT-002).
	if err := CheckJSONDepth(body, maxJSONDepth, maxFieldCount); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedInput, err)
	}
	var req struct {
		Model        string          `json:"model"`
		Input        json.RawMessage `json:"input"`
		Instructions json.RawMessage `json:"instructions"`
		Tools        []struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"tools"`
		PreviousResponseID *string `json:"previous_response_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON body: %v", ErrUnsupportedInput, err)
	}
	if req.PreviousResponseID != nil && *req.PreviousResponseID != "" {
		doc.MissingParts = append(doc.MissingParts, "previous_response_id:"+*req.PreviousResponseID)
		doc.Coverage = CoverageUnsupported
	}

	if len(req.Instructions) > 0 && string(req.Instructions) != "null" {
		var s string
		if err := json.Unmarshal(req.Instructions, &s); err == nil {
			doc.add("system", "instructions", "text", s)
		} else {
			doc.addJSONSurface("system", "instructions", req.Instructions)
		}
	}

	// input: string | content-part array | item array
	var single string
	if err := json.Unmarshal(req.Input, &single); err == nil {
		doc.add("user", "input", "text", single)
	} else {
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(req.Input, &items); err != nil {
			if req.Input != nil && string(req.Input) != "null" {
				return nil, fmt.Errorf("%w: input must be string or array", ErrUnsupportedInput)
			}
			items = nil
		}
		for _, item := range items {
			if err := doc.extractInputItem(item, strict); err != nil {
				return nil, err
			}
		}
	}

	for _, tool := range req.Tools {
		if tool.Description != "" {
			doc.add("developer", "tool_definition", "text", tool.Description)
		}
		if len(tool.Parameters) > 0 && string(tool.Parameters) != "null" {
			doc.addJSONSurface("developer", "tool_definition", tool.Parameters)
		}
	}

	// Images are recorded as refs; whether they are auditable is decided by
	// the moderation adapter (§5), not assumed here.
	doc.finalize(strict)
	return doc, nil
}

func (d *AuditDocument) extractInputItem(item map[string]json.RawMessage, strict bool) error {
	typeVal := jsonString(item["type"])
	role := jsonString(item["role"])
	switch typeVal {
	case "", "message":
		content := item["content"]
		var s string
		if err := json.Unmarshal(content, &s); err == nil {
			d.add(roleOr(role), "input", "text", s)
			return nil
		}
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(content, &parts); err != nil {
			if content != nil && string(content) != "null" {
				return fmt.Errorf("%w: message content must be string or part array", ErrUnsupportedInput)
			}
			return nil
		}
		for _, p := range parts {
			switch jsonString(p["type"]) {
			case "input_text", "output_text", "text", "refusal":
				d.add(roleOr(role), "input", "text", jsonString(p["text"]))
			case "input_image", "image_url":
				ref := jsonString(p["image_url"])
				if ref == "" {
					ref = jsonString(p["url"])
				}
				d.addImage(roleOr(role), "input", ref)
			default:
				if strict {
					return fmt.Errorf("%w: unsupported content part type %q", ErrUnsupportedInput, jsonString(p["type"]))
				}
				d.MissingParts = append(d.MissingParts, "content_part:"+jsonString(p["type"]))
				d.Coverage = CoveragePartial
			}
		}
	case "function_call":
		d.add("assistant", "tool_call", "text", jsonString(item["name"]))
		d.addJSONSurface("assistant", "tool_call", item["arguments"])
	case "function_call_output":
		d.addJSONSurface("tool", "tool_result", item["output"])
	case "item_reference":
		// references server-side state the gateway cannot see (§6.1)
		d.MissingParts = append(d.MissingParts, "item_reference:"+jsonString(item["id"]))
		d.Coverage = CoverageUnsupported
	default:
		if strict {
			return fmt.Errorf("%w: unsupported input item type %q", ErrUnsupportedInput, typeVal)
		}
		d.MissingParts = append(d.MissingParts, "input_item:"+typeVal)
		d.Coverage = CoveragePartial
	}
	return nil
}

func roleOr(r string) string {
	if r == "" {
		return "user"
	}
	return r
}

func jsonString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// addJSONSurface extracts text from JSON tool arguments/results within a
// bounded structure (§21: 不解析执行代码). String leaves become text segments;
// the JSON is kept as one segment so rule scope tool_result applies.
func (d *AuditDocument) addJSONSurface(role, source string, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		d.add(role, source, "text", s)
		return
	}
	// walk string leaves with a bounded tokenizer instead of recursive Unmarshal
	texts := extractStringsBounded(raw, 0, maxJSONDepth, maxFieldCount)
	joined := strings.Join(texts, "\n")
	d.add(role, source, "json", joined)
}

// extractStringsBounded walks a JSON value and returns every string leaf,
// refusing deeply nested or oversized structures.
func extractStringsBounded(raw json.RawMessage, depth, maxDepth, maxFields int) []string {
	if depth > maxDepth || len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	var out []string
	fields := 0
	var walk func(dec *json.Decoder, depth int) error
	walk = func(dec *json.Decoder, depth int) error {
		if depth > maxDepth {
			return fmt.Errorf("depth limit %d", maxDepth)
		}
		for {
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := tok.(json.Delim); ok {
				switch d {
				case '{', '[':
					if err := walk(dec, depth+1); err != nil {
						return err
					}
				default:
					return nil // closing delim
				}
				continue
			}
			fields++
			if fields > maxFields {
				return fmt.Errorf("field limit %d", maxFields)
			}
			if s, ok := tok.(string); ok {
				if utf8.RuneCountInString(s) <= maxStringValue {
					out = append(out, s)
				}
			}
		}
	}
	// top-level: array/object/string handled uniformly
	switch t := tok.(type) {
	case json.Delim:
		if err := walk(dec, 1); err != nil {
			return nil
		}
	case string:
		out = append(out, t)
	}
	return out
}

// CheckJSONDepth validates the body decodes and stays within depth/field
// bounds (FORMAT-002) without ever materialising the full object tree.
func CheckJSONDepth(body []byte, maxDepth, maxFields int) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var fields int
	var walk func(dec *json.Decoder, depth int) error
	walk = func(dec *json.Decoder, depth int) error {
		if depth > maxDepth {
			return fmt.Errorf("JSON nesting exceeds depth limit %d", maxDepth)
		}
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if d, ok := tok.(json.Delim); ok {
				switch d {
				case '{', '[':
					if err := walk(dec, depth+1); err != nil {
						return err
					}
				default:
					return nil
				}
				continue
			}
			fields++
			if fields > maxFields {
				return fmt.Errorf("JSON field count exceeds limit %d", maxFields)
			}
		}
	}
	return walk(dec, 0)
}

func (d *AuditDocument) add(role, source, typ, text string) {
	if text == "" {
		return
	}
	d.Segments = append(d.Segments, Segment{Role: role, Source: source, Type: typ, Text: text, Position: len(d.Segments)})
}

func (d *AuditDocument) addImage(role, source, ref string) {
	if ref == "" {
		return
	}
	d.Segments = append(d.Segments, Segment{Role: role, Source: source, Type: "image_ref", ImageRef: ref, Position: len(d.Segments)})
}

// finalize computes the coverage verdict (§21): unsupported wins, partial
// when anything was missing, full_visible only when everything visible was
// extracted.
func (d *AuditDocument) finalize(strict bool) {
	if d.Coverage != CoverageUnsupported {
		if len(d.MissingParts) > 0 {
			d.Coverage = CoveragePartial
		} else {
			d.Coverage = CoverageFull
		}
	}
}
