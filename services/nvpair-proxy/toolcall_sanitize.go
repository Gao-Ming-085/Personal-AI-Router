// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// Response sanitization for engines whose chat template can emit tool-call
// markup for undeclared tool names: the engine returns that markup as plain
// answer text and flags the reply with `strata_rejected_tool_calls`. A
// client that ignores the flag re-ingests its own control tokens as
// conversation content. This sanitizer stops that for every consumer of the
// facade — local plaintext clients and mTLS cluster peers alike. The
// semantics port the reference Python guard that ran in front of such
// engines for months: non-streaming replies carrying the flag have it
// stripped, leaked blocks cleaned from every choice's content, and a short
// note substituted when nothing valid remains. Streaming deltas pass through
// until the flag or a plausible marker opening appears; from there the tail
// is held (a trailing partial marker prefix is withheld), and at stream end
// a flagged reply is flushed cleaned — the note standing in when nothing
// valid remains — before the stream is terminated with [DONE]. An unflagged
// stream never drops held text, and anything the sanitizer cannot parse
// passes through byte-for-byte. The one deliberate divergence from the guard
// is its single-replay recovery (a nudged re-request when a flagged answer
// came back empty): replaying a generation inside the proxy hides cost and
// latency from the client, so a note is delivered instead. Scope is per
// engine profile via engineProfile.SanitizeToolCallLeaks; the
// llama.cpp/Ollama/vLLM facades keep verbatim passthrough behavior.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	leakFlagKey = "strata_rejected_tool_calls"
	leakNote    = "(剛才我發出的工具呼叫格式無效，已被擋下，所以那一輪沒有執行任何工具。我改用既有工具或直接以文字說明。)"
	leakOpen    = "<tool_call"
)

var (
	// leakSuspicionRe is a plausible marker opening: any control markup
	// family the templates can leak.
	leakSuspicionRe = regexp.MustCompile(`(?i)<\s*(?:tool_call|function=|invoke\b|antml:[a-z_]+)`)

	// leakBlockRe removes leaked blocks and an unterminated run to the end
	// of the content. RE2 has no backreferences, so the name families the
	// guard paired dynamically are enumerated (each opening closes with its
	// own name, or runs to the end).
	leakBlockRe = regexp.MustCompile(`(?is)` +
		`<\s*antml:tool_call\b.*?(?:<\s*/\s*antml:tool_call\s*>|$)` +
		`|<\s*(?:tool_call|function_call)\b.*?(?:<\s*/\s*(?:tool_call|function_call)\s*>|$)`)
)

// cleanLeakedMarkup strips tool-call markup from answer content.
func cleanLeakedMarkup(s string) string {
	return strings.TrimSpace(leakBlockRe.ReplaceAllString(s, ""))
}

// leakHoldTailLen is the length of the tail of s that could still become the
// leakOpen marker once more content arrives; that many trailing bytes must
// be withheld from an otherwise safe flush.
func leakHoldTailLen(s string) int {
	n := min(len(s), len(leakOpen)-1)
	for n > 0 {
		if strings.HasSuffix(s, leakOpen[:n]) {
			return n
		}
		n--
	}
	return 0
}

func decodeJSONObj(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var obj map[string]any
	err := dec.Decode(&obj)
	return obj, err
}

// marshalNoHTMLEscape serializes like the guard's ensure_ascii=False output:
// raw UTF-8, angle brackets and ampersands left literal.
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// truthyJSON mirrors the guard's `flagged = evt.pop(key) or flagged`.
func truthyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		return t.String() != "0" && t.String() != "0.0"
	default:
		return true
	}
}

// sanitizeChatJSONBody applies the non-streaming rule. Bytes are returned
// unchanged unless a truthy upstream flag is present and parses.
func sanitizeChatJSONBody(body []byte) ([]byte, bool) {
	if !bytes.Contains(body, []byte(leakFlagKey)) {
		return body, false
	}
	obj, err := decodeJSONObj(body)
	if err != nil {
		return body, false
	}
	flag, ok := obj[leakFlagKey]
	if !ok || !truthyJSON(flag) {
		return body, false
	}
	delete(obj, leakFlagKey)
	choiceList, _ := obj["choices"].([]any)
	valid := false
	for _, c := range choiceList {
		choice, _ := c.(map[string]any)
		if choice == nil {
			continue
		}
		msg, _ := choice["message"].(map[string]any)
		if msg == nil {
			msg = map[string]any{}
			choice["message"] = msg
		}
		content, _ := msg["content"].(string)
		msg["content"] = cleanLeakedMarkup(content)
		if l, _ := msg["tool_calls"].([]any); msg["content"] != "" || len(l) > 0 {
			valid = true
		}
	}
	if !valid && len(choiceList) > 0 {
		if choice, _ := choiceList[0].(map[string]any); choice != nil {
			msg, _ := choice["message"].(map[string]any)
			if msg == nil {
				msg = map[string]any{}
				choice["message"] = msg
			}
			msg["content"] = leakNote
		}
	}
	out, err := marshalNoHTMLEscape(obj)
	if err != nil {
		return body, false
	}
	return out, true
}

// leakSanitizer wraps an http.ResponseWriter for one proxied engine
// response. The class is decided at WriteHeader from status and
// Content-Type; everything else streams through with the least
// interference possible.
type leakSanitizer struct {
	rw   http.ResponseWriter
	mode int
	code int
	body []byte   // json mode accumulation
	sse  *leakSSE // sse mode state
	done bool
}

const (
	leakModePending = iota
	leakModeRaw
	leakModeJSON
	leakModeSSE
)

func newLeakSanitizer(w http.ResponseWriter) *leakSanitizer {
	return &leakSanitizer{rw: w, mode: leakModePending}
}

// Unwrap keeps http.ResponseController (flush, deadlines, hijack) wired to
// the real writer, mirroring statusCapture.
func (s *leakSanitizer) Unwrap() http.ResponseWriter { return s.rw }

func (s *leakSanitizer) Header() http.Header { return s.rw.Header() }

func (s *leakSanitizer) WriteHeader(code int) {
	if s.mode != leakModePending {
		return
	}
	s.code = code
	ct := s.rw.Header().Get("Content-Type")
	switch {
	case code == http.StatusOK && strings.Contains(ct, "text/event-stream"):
		s.mode = leakModeSSE
		s.sse = newLeakSSE(s.rw)
		s.rw.WriteHeader(code)
		s.flushUnderlying()
	case code == http.StatusOK && strings.Contains(ct, "application/json"):
		// Buffered: the final Content-Length replaces the upstream's at
		// finish() time.
		s.mode = leakModeJSON
		s.rw.Header().Del("Content-Length")
	default:
		s.mode = leakModeRaw
		s.done = true
		s.rw.WriteHeader(code)
	}
}

func (s *leakSanitizer) Write(p []byte) (int, error) {
	if s.mode == leakModePending {
		// A body with no headers of our own to classify: pass through.
		s.mode = leakModeRaw
		s.done = true
		s.rw.WriteHeader(http.StatusOK)
	}
	switch s.mode {
	case leakModeJSON:
		s.body = append(s.body, p...)
		return len(p), nil
	case leakModeSSE:
		s.sse.feed(p)
		return len(p), nil
	default:
		return s.rw.Write(p)
	}
}

// Flush implements http.Flusher. Buffered JSON has nothing to flush (it is
// delivered at finish()); SSE already flushed on each emitted event.
func (s *leakSanitizer) Flush() {
	if s.mode == leakModeRaw || s.mode == leakModePending {
		s.flushUnderlying()
	}
}

func (s *leakSanitizer) flushUnderlying() {
	if f, ok := s.rw.(http.Flusher); ok {
		f.Flush()
	}
}

// finish delivers the buffered JSON body or closes out a held SSE tail. It
// must run after the reverse proxy has copied the upstream body and before
// the handler returns; it is safe to call more than once.
func (s *leakSanitizer) finish() {
	switch s.mode {
	case leakModeJSON:
		if s.done {
			return
		}
		s.done = true
		out, changed := sanitizeChatJSONBody(s.body)
		if changed {
			slog.Info("stripped stray tool-call markup from engine reply")
		}
		s.rw.Header().Set("Content-Length", strconv.Itoa(len(out)))
		s.rw.WriteHeader(s.code)
		_, _ = s.rw.Write(out)
	case leakModeSSE:
		if !s.done {
			s.done = true
			s.sse.finalize()
		}
	default:
		s.done = true
	}
}

// leakSSE ports the guard's stream filter: content deltas pass through
// until the flag or a plausible marker opening appears, from where the raw
// tail lines are held; content accumulates in full so the end of the stream
// can flush a cleaned remainder (or substitute the note) and terminate with
// [DONE]. A partial marker prefix at the safe boundary is withheld until
// more content decides what it is.
type leakSSE struct {
	rw       http.ResponseWriter
	carry    []byte // bytes since the last '\n' (upstream chunk may split a line)
	full     strings.Builder
	em       int // bytes of full already forwarded to the client
	hold     bool
	flagged  bool
	tools    bool
	tmpl     map[string]any // last flag-stripped event: template for synthesized chunks
	tail     [][]byte       // raw lines withheld since the hold began
	doneSeen bool           // upstream's own [DONE] was consumed
}

func newLeakSSE(rw http.ResponseWriter) *leakSSE { return &leakSSE{rw: rw} }

func (s *leakSSE) feed(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.carry = append(s.carry, p...)
			return
		}
		s.processLine(append(s.carry, p[:i]...))
		s.carry = s.carry[:0]
		p = p[i+1:]
	}
}

// finalize closes the stream the way the guard does: flush or replace the
// held content depending on the flag, deliver withheld tail lines with the
// flag stripped, then terminate with [DONE].
func (s *leakSSE) finalize() {
	full := s.full.String()
	switch {
	case s.flagged:
		slog.Info("stripped stray tool-call markup from streamed engine reply")
		kept := cleanLeakedMarkup(full)
		if kept != "" {
			rest := kept
			if s.em <= len(kept) && s.em <= len(full) && kept[:s.em] == full[:s.em] {
				rest = kept[s.em:]
			}
			if rest != "" {
				s.emitChunk(rest)
			}
		} else if !s.tools {
			s.emitChunk(leakNote)
		}
	case s.hold && len(full) > s.em:
		s.emitChunk(full[s.em:])
	}
	for _, line := range s.tail {
		s.emit(line)
	}
	// A full event boundary: [DONE] plus its framing blank line. Every
	// emission above ended on a line boundary, so no leading newline is
	// needed to close a partial line.
	s.emit(append([]byte("data: [DONE]"), '\n'))
}

// processLine handles one physical line of the upstream stream. raw has no
// trailing newline.
func (s *leakSSE) processLine(raw []byte) {
	if !bytes.HasPrefix(raw, []byte("data:")) {
		s.route(raw)
		return
	}
	blob := bytes.TrimSpace(raw[5:])
	if string(blob) == "[DONE]" {
		// finalize emits the terminating [DONE] after any released tail;
		// its framing blank line is swallowed with it below.
		s.doneSeen = true
		return
	}
	obj, err := decodeJSONObj(blob)
	if err != nil {
		s.route(raw)
		return
	}
	rewritten := false
	if v, ok := obj[leakFlagKey]; ok {
		if truthyJSON(v) {
			s.flagged = true
		}
		delete(obj, leakFlagKey)
		rewritten = true
	}
	s.tmpl = obj // post-flag-strip: synthesized chunks never carry it

	var choice map[string]any
	if l, _ := obj["choices"].([]any); len(l) > 0 {
		choice, _ = l[0].(map[string]any)
	}
	var delta map[string]any
	var content string
	finish, _ := choice["finish_reason"].(string)
	if choice != nil {
		delta, _ = choice["delta"].(map[string]any)
		if l, _ := delta["tool_calls"].([]any); len(l) > 0 || finish == "tool_calls" {
			s.tools = true
		}
		if delta != nil {
			content, _ = delta["content"].(string)
		}
	}
	if content == "" {
		if rewritten {
			if b, err := marshalNoHTMLEscape(obj); err == nil {
				raw = append([]byte("data: "), b...)
			}
		}
		s.route(raw)
		return
	}

	// Content delta: never forwarded verbatim once held; the guard always
	// re-serializes these, but an untouched byte-identical event is kept
	// raw so normal streams are bit-for-bit passthrough.
	s.full.WriteString(content)
	if s.hold {
		return
	}
	full := s.full.String()
	safe := len(full)
	if m := leakSuspicionRe.FindStringIndex(full); m != nil {
		s.hold = true
		safe = m[0]
	} else {
		safe = len(full) - leakHoldTailLen(full)
	}
	if safe <= s.em {
		return
	}
	keep := full[s.em:safe]
	if !rewritten && keep == content {
		// Emit directly: a hold that began on this very event must still
		// release the safe prefix computed above (the guard writes the
		// shrunken chunk directly, never through the tail route).
		s.emit(raw)
		s.em = safe
		return
	}
	evt := map[string]any{}
	for k, v := range s.tmpl {
		evt[k] = v
	}
	evt["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"content": keep}}}
	if b, err := marshalNoHTMLEscape(evt); err == nil {
		s.emit(append([]byte("data: "), b...))
		s.em = safe
	}
}

func (s *leakSSE) route(raw []byte) {
	if s.doneSeen && len(bytes.TrimSpace(raw)) == 0 {
		return // framing of the consumed [DONE]
	}
	if s.hold {
		s.tail = append(s.tail, raw)
		return
	}
	s.emit(raw)
}

func (s *leakSSE) emit(raw []byte) {
	buf := append(raw, '\n')
	if _, err := s.rw.Write(buf); err != nil {
		return
	}
	if err := http.NewResponseController(s.rw).Flush(); err != nil {
		return
	}
}

// emitChunk synthesizes one content event from the last seen envelope, the
// guard's chunk() equivalent.
func (s *leakSSE) emitChunk(text string) {
	evt := map[string]any{}
	for k, v := range s.tmpl {
		evt[k] = v
	}
	evt["choices"] = []any{map[string]any{
		"index":         0,
		"delta":         map[string]any{"content": text},
		"finish_reason": nil,
	}}
	b, err := marshalNoHTMLEscape(evt)
	if err != nil {
		return
	}
	buf := append([]byte("data: "), b...)
	buf = append(buf, '\n', '\n')
	if _, err := s.rw.Write(buf); err != nil {
		return
	}
	_ = http.NewResponseController(s.rw).Flush()
}
