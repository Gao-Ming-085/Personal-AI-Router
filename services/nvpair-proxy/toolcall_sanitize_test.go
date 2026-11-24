// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCleanLeakedMarkup(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"plain prose", "plain prose"},
		{"pre <tool_call\nget_weather(x)\n</TOOL_CALL> post", "pre  post"},
		{"<tool_call\na(1)\n</tool_call>", ""},
		{"tail after block <tool_call\nb()", "tail after block"},
		{"keep <invoke name=\"x\">", "keep <invoke name=\"x\">"},
		{"<antml:tool_call>\nx\n</antml:tool_call>real", "real"},
	} {
		if got := cleanLeakedMarkup(tt.in); got != tt.want {
			t.Errorf("cleanLeakedMarkup(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLeakHoldTailLen(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int
	}{
		{"hello", 0},
		{"hello <too", 4},
		{"<tool_call", 0}, // full marker: suspicion handles it, nothing to withhold
		{"a<", 1},
	} {
		if got := leakHoldTailLen(tt.in); got != tt.want {
			t.Errorf("leakHoldTailLen(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeChatJSONBody(t *testing.T) {
	plain := []byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	if out, changed := sanitizeChatJSONBody(plain); changed || string(out) != string(plain) {
		t.Errorf("unflagged body must pass through unchanged: %q", out)
	}
	flaggedGarbage := []byte(`{"strata_rejected_tool_calls":3,"choices":[{"message":{"role":"assistant","content":"<tool_call\nget_weather(x)\n</tool_call>"}}]}`)
	out, changed := sanitizeChatJSONBody(flaggedGarbage)
	if !changed {
		t.Fatal("flagged body must be sanitized")
	}
	s := string(out)
	if strings.Contains(s, leakFlagKey) || strings.Contains(s, "<tool_call") {
		t.Errorf("flag/markup survived: %s", s)
	}
	if !strings.Contains(s, leakNote) {
		t.Errorf("note missing: %s", s)
	}
	flaggedProse := []byte(`{"strata_rejected_tool_calls":1,"choices":[{"message":{"role":"assistant","content":"Answer: 42. <tool_call\nfoo()</tool_call>"}}]}`)
	out, _ = sanitizeChatJSONBody(flaggedProse)
	if !strings.Contains(string(out), "Answer: 42.") || strings.Contains(string(out), leakNote) {
		t.Errorf("prose should survive cleaning without note: %s", out)
	}
	flaggedTools := []byte(`{"strata_rejected_tool_calls":1,"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"1"}]}}]}`)
	out, _ = sanitizeChatJSONBody(flaggedTools)
	if strings.Contains(string(out), leakNote) {
		t.Errorf("valid tool_calls must suppress the note: %s", out)
	}
	falsy := []byte(`{"strata_rejected_tool_calls":0,"choices":[{"message":{"content":"ok"}}]}`)
	if _, changed := sanitizeChatJSONBody(falsy); changed {
		t.Error("a falsy flag must not trigger sanitization")
	}
}

// leakProxyHandler mirrors the facade wiring: the sanitizer wraps the
// reverse proxy's writer, with finish() deferred in the handler.
func leakProxyHandler(t *testing.T, upstreamURL string) http.HandlerFunc {
	t.Helper()
	u, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	return func(w http.ResponseWriter, r *http.Request) {
		sw := newLeakSanitizer(w)
		defer sw.finish()
		proxy.ServeHTTP(sw, r)
	}
}

func getBody(t *testing.T, url string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" && cl != fmt.Sprint(len(b)) {
		t.Errorf("Content-Length %s but read %d bytes", cl, len(b))
	}
	return resp.StatusCode, resp.Header, string(b)
}

func TestJSONFlaggedEndToEnd(t *testing.T) {
	body := `{"strata_rejected_tool_calls":2,"choices":[{"message":{"role":"assistant","content":"<tool_call>x()</tool_call>"}}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	code, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	if code != 200 || strings.Contains(got, leakFlagKey) || strings.Contains(got, "<tool_call") || !strings.Contains(got, leakNote) {
		t.Errorf("bad sanitized reply: %d %s", code, got)
	}
}

func TestJSONPassthroughEndToEnd(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"all good"}}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	_, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	if got != body {
		t.Errorf("unflagged JSON must be byte-identical, got %q", got)
	}
}

func TestErrorPassthroughEndToEnd(t *testing.T) {
	body := `{"error":{"message":"model not loaded"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		fmt.Fprint(w, body)
	}))
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	code, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	if code != 500 || got != body {
		t.Errorf("error body must pass through: %d %q", code, got)
	}
}

func sseServer(t *testing.T, script []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream writer is not a flusher")
		}
		for _, s := range script {
			if _, err := io.WriteString(w, s); err != nil {
				return
			}
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
}

func contentEvt(text string) string {
	return `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"` + text + `"}}]}` + "\n\n"
}

func TestSSEPlainPassthrough(t *testing.T) {
	script := []string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n",
		contentEvt("Hello"),
		contentEvt(" world"),
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"finish_reason":"stop"},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	up := sseServer(t, script)
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	_, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	if got != strings.Join(script, "") {
		t.Errorf("plain stream must be byte-identical:\n got %q\nwant %q", got, strings.Join(script, ""))
	}
}

func TestSSESplitMarkerRejoins(t *testing.T) {
	script := []string{
		contentEvt("The result: "),
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"<tool_`,
		`call get()}"}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	up := sseServer(t, script)
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	_, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	// Unflagged: nothing may be lost. The rejoined marker held a suspect
	// tail, released verbatim as one synthesized chunk at stream end.
	if !strings.Contains(got, `"content":"The result: "`) {
		t.Errorf("safe prefix missing: %s", got)
	}
	if !strings.Contains(got, `"content":"<tool_call get()}"`) {
		t.Errorf("held tail not released as one chunk: %s", got)
	}
	if !strings.Contains(got, "data: [DONE]") {
		t.Errorf("stream must be terminated: %s", got)
	}
}

func TestSSEFlaggedNoteWhenEmpty(t *testing.T) {
	script := []string{
		contentEvt(`<tool_call>\nget_weather()\n</tool_call>`),
		`data: {"id":"c1","strata_rejected_tool_calls":3,"choices":[{"index":0,"delta":{"finish_reason":"stop"}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	up := sseServer(t, script)
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	_, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	if strings.Contains(got, leakFlagKey) {
		t.Errorf("flag leaked to client: %s", got)
	}
	if strings.Contains(got, "<tool_call") {
		t.Errorf("markup leaked to client: %s", got)
	}
	if !strings.Contains(got, leakNote) || !strings.Contains(got, "data: [DONE]") {
		t.Errorf("note or terminator missing: %s", got)
	}
}

func TestSSEFlaggedKeepsProse(t *testing.T) {
	script := []string{
		contentEvt("Answer: 42."),
		contentEvt(`<tool_call\nfoo()`),
		`data: {"id":"c1","strata_rejected_tool_calls":1,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	up := sseServer(t, script)
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	_, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	if strings.Contains(got, leakFlagKey) || strings.Contains(got, "<tool_call") {
		t.Errorf("flag or markup leaked: %s", got)
	}
	if !strings.Contains(got, `"content":"Answer: 42."`) {
		t.Errorf("prose before the marker must stream through: %s", got)
	}
	if strings.Contains(got, leakNote) {
		t.Errorf("note must not replace surviving prose: %s", got)
	}
	if !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Errorf("held finish event must be released: %s", got)
	}
}

func TestSSEUnclosedStreamStillTerminates(t *testing.T) {
	// Upstream dies mid-generation without [DONE]: the client must still
	// see a terminated stream.
	script := []string{contentEvt("The "), contentEvt("<tool")}
	up := sseServer(t, script)
	defer up.Close()
	srv := httptest.NewServer(leakProxyHandler(t, up.URL))
	defer srv.Close()
	_, _, got := getBody(t, srv.URL+"/v1/chat/completions")
	if !strings.Contains(got, "data: [DONE]") {
		t.Errorf("stream must be terminated on upstream close: %s", got)
	}
	// A trailing partial marker of an UNFLAGGED reply is withheld exactly
	// as long as the guard withholds it: never shown, never fabricated.
	if strings.Contains(got, "<tool") {
		t.Errorf("partial marker must stay withheld: %s", got)
	}
}
