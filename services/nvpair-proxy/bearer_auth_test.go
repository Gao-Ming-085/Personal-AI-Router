// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBearerAuthTransport_StampsKeyAndReplacesCallerAuth(t *testing.T) {
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	client := &http.Client{Transport: &bearerAuthTransport{
		next:   http.DefaultTransport,
		apiKey: "sk-node",
	}}
	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer sk-caller")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "Bearer sk-node", got, "engine key must replace the caller's")
}

func TestCandidateRoundTripper_WrapsOnlyKeyedManualCandidates(t *testing.T) {
	p := testProxy(anyProfile(t), NewDiscovery(), 11435)

	plain := p.candidateRoundTripper(candidate{id: "manual-no-key"})
	assert.Same(t, p.candidateTransport(candidate{}), plain,
		"a keyless candidate must keep the pooled Transport identity")

	keyed := p.candidateRoundTripper(candidate{id: "manual-keyed", apiKey: "sk-x"})
	wrapped, ok := keyed.(*bearerAuthTransport)
	require.True(t, ok, "keyed candidate not wrapped with the bearer transport")
	assert.Equal(t, "sk-x", wrapped.apiKey)
	assert.NotSame(t, plain, http.RoundTripper(keyed))

	u, err := url.Parse("http://127.0.0.1:1")
	require.NoError(t, err)
	keyedCandidate := candidate{url: u, apiKey: "sk-x"}
	assert.NotSame(t, p.candidateTransport(keyedCandidate),
		http.RoundTripper(p.candidateRoundTripper(keyedCandidate)))
}
