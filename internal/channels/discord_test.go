//
//  Copyright 2026 The InfiniFlow Authors. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ragflow/internal/channels/core"
)

func TestNewDiscordChannelFromConfigRequiresToken(t *testing.T) {
	if _, err := newDiscordChannelFromConfig("account-1", map[string]any{}); err == nil {
		t.Fatal("newDiscordChannelFromConfig succeeded without token")
	}
}

func TestNewDiscordChannelFromConfigNormalizesConfig(t *testing.T) {
	ch, err := newDiscordChannelFromConfig("account-1", map[string]any{
		"token":           "Bot token-1",
		"api_base_url":    "https://discord.example/api",
		"gateway_url":     "wss://gateway.example",
		"timeout_secs":    "7",
		"gateway_intents": "4096",
	})
	if err != nil {
		t.Fatalf("newDiscordChannelFromConfig returned error: %v", err)
	}
	if ch.account.Token != "token-1" {
		t.Fatalf("token = %q, want %q", ch.account.Token, "token-1")
	}
	if ch.account.APIBaseURL != "https://discord.example/api" {
		t.Fatalf("api base URL = %q", ch.account.APIBaseURL)
	}
	if ch.account.GatewayURL != "wss://gateway.example" {
		t.Fatalf("gateway URL = %q", ch.account.GatewayURL)
	}
	if ch.account.Timeout != 7*time.Second {
		t.Fatalf("timeout = %s, want 7s", ch.account.Timeout)
	}
	if ch.account.Intents != 4096 {
		t.Fatalf("intents = %d, want 4096", ch.account.Intents)
	}
}

func TestDiscordChatType(t *testing.T) {
	dm := 1
	thread := 11
	group := 0

	tests := []struct {
		name        string
		guildID     string
		channelType *int
		want        string
	}{
		{name: "dm channel", channelType: &dm, want: "p2p"},
		{name: "thread channel", guildID: "guild-1", channelType: &thread, want: "thread"},
		{name: "guild text channel", guildID: "guild-1", channelType: &group, want: "group"},
		{name: "guild fallback", guildID: "guild-1", want: "group"},
		{name: "direct fallback", want: "p2p"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := discordChatType(tt.guildID, tt.channelType); got != tt.want {
				t.Fatalf("discordChatType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiscordEnqueueDoesNotMarkDroppedMessageSeen(t *testing.T) {
	ch := newDiscordChannel(discordAccount{AccountID: "account-1", Token: "token-1"})
	worker := &discordChatWorker{queue: make(chan core.IncomingMessage, 1)}
	worker.queue <- core.IncomingMessage{MessageID: "queued"}
	ch.workers["chat-1"] = worker

	ok := ch.enqueueIncoming(context.Background(), core.IncomingMessage{
		ChatID:    "chat-1",
		MessageID: "dropped",
	})

	if ok {
		t.Fatal("enqueueIncoming succeeded for a full queue")
	}
	if _, seen := ch.seen["dropped"]; seen {
		t.Fatal("dropped message was marked seen")
	}
}

func TestDiscordRequestJSONReturnsRateLimitError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "0.001")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited","retry_after":0.001,"global":true}`))
	}))
	defer server.Close()
	allowChannelURLForTest(t, server.URL)

	ch := newDiscordChannel(discordAccount{
		AccountID:  "account-1",
		Token:      "token-1",
		APIBaseURL: server.URL,
	})

	err := ch.requestJSON(context.Background(), http.MethodGet, "/gateway/bot", nil, nil)
	var rateLimitErr *discordRateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("requestJSON error = %v, want discordRateLimitError", err)
	}
	if rateLimitErr.RetryAfter != time.Millisecond {
		t.Fatalf("retry after = %s, want 1ms", rateLimitErr.RetryAfter)
	}
	if !rateLimitErr.Global {
		t.Fatal("global rate limit flag was not preserved")
	}
}

func TestDiscordSendRetriesRateLimit(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/channels/chat-1/messages" {
			t.Fatalf("request path = %q, want /channels/chat-1/messages", got)
		}
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited","retry_after":0.001}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"message-2"}`))
	}))
	defer server.Close()
	allowChannelURLForTest(t, server.URL)

	ch := newDiscordChannel(discordAccount{
		AccountID:  "account-1",
		Token:      "token-1",
		APIBaseURL: server.URL,
	})

	err := ch.Send(context.Background(), core.OutgoingMessage{
		ChatID: "chat-1",
		Text:   "hello",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestSplitDiscordMessage(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{name: "short", text: "hello", limit: 10, want: []string{"hello"}},
		{name: "exact", text: "0123456789", limit: 10, want: []string{"0123456789"}},
		{name: "newline", text: "line one\nline two", limit: 12, want: []string{"line one", "line two"}},
		{name: "space", text: "alpha beta gamma", limit: 11, want: []string{"alpha beta", "gamma"}},
		{name: "hard", text: "abcdefghij", limit: 4, want: []string{"abcd", "efgh", "ij"}},
		{name: "multibyte", text: "ééééé", limit: 2, want: []string{"éé", "éé", "é"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitDiscordMessage(tc.text, tc.limit)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
				t.Fatalf("splitDiscordMessage(%q, %d) = %q, want %q", tc.text, tc.limit, got, tc.want)
			}
		})
	}
}

func TestDiscordSendSplitsLongMessages(t *testing.T) {
	var (
		mu       sync.Mutex
		payloads []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		mu.Lock()
		payloads = append(payloads, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"message"}`))
	}))
	defer server.Close()
	allowChannelURLForTest(t, server.URL)

	ch := newDiscordChannel(discordAccount{
		AccountID:  "account-1",
		Token:      "token-1",
		APIBaseURL: server.URL,
	})

	text := strings.Repeat("a", discordMaxMessageLength) + "\n" + strings.Repeat("b", 10)
	if err := ch.Send(context.Background(), core.OutgoingMessage{
		ChatID:           "chat-1",
		Text:             text,
		ReplyToMessageID: "incoming-1",
	}); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 2 {
		t.Fatalf("posted %d messages, want 2", len(payloads))
	}
	for i, payload := range payloads {
		content, _ := payload["content"].(string)
		if n := len([]rune(content)); n > discordMaxMessageLength {
			t.Fatalf("message %d has %d characters, want <= %d", i, n, discordMaxMessageLength)
		}
	}
	if _, ok := payloads[0]["message_reference"]; !ok {
		t.Fatal("first message should reply to the incoming message")
	}
	if _, ok := payloads[1]["message_reference"]; ok {
		t.Fatal("follow-up messages should not carry a message reference")
	}
	if payloads[1]["content"] != strings.Repeat("b", 10) {
		t.Fatalf("second message content = %q", payloads[1]["content"])
	}
}
