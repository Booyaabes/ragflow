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

func TestSplitDiscordMessageWithinLimitIsUnchanged(t *testing.T) {
	text := "short answer"
	got := splitDiscordMessage(text, 2000)
	if len(got) != 1 || got[0] != text {
		t.Fatalf("splitDiscordMessage() = %#v, want single chunk %q", got, text)
	}
}

func TestSplitDiscordMessageBreaksOnNewline(t *testing.T) {
	text := strings.Repeat("a", 5) + "\n" + strings.Repeat("b", 5)
	got := splitDiscordMessage(text, 6)
	want := []string{strings.Repeat("a", 5), strings.Repeat("b", 5)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("splitDiscordMessage() = %#v, want %#v", got, want)
	}
}

func TestSplitDiscordMessageHardCutsLongWord(t *testing.T) {
	text := strings.Repeat("x", 10)
	got := splitDiscordMessage(text, 4)
	want := []string{"xxxx", "xxxx", "xx"}
	if len(got) != len(want) {
		t.Fatalf("splitDiscordMessage() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitDiscordMessage()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitDiscordMessagePreservesMultiByteRunes(t *testing.T) {
	text := strings.Repeat("é", 10)
	got := splitDiscordMessage(text, 4)
	if joined := strings.Join(got, ""); joined != text {
		t.Fatalf("splitDiscordMessage() lost content: got %q, want %q", joined, text)
	}
	for _, chunk := range got {
		if len([]rune(chunk)) > 4 {
			t.Fatalf("chunk %q exceeds limit of 4 runes", chunk)
		}
	}
}

func TestDiscordSendSplitsLongMessageAcrossMultipleRequests(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"message-1"}`))
	}))
	defer server.Close()

	ch := newDiscordChannel(discordAccount{
		AccountID:  "account-1",
		Token:      "token-1",
		APIBaseURL: server.URL,
	})

	longText := strings.Repeat("word ", 500) // well over discordMaxMessageLength
	err := ch.Send(context.Background(), core.OutgoingMessage{
		ChatID:           "chat-1",
		Text:             longText,
		ReplyToMessageID: "orig-1",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if len(requests) < 2 {
		t.Fatalf("expected multiple requests for a long message, got %d", len(requests))
	}
	for i, req := range requests {
		content, _ := req["content"].(string)
		if len([]rune(content)) > discordMaxMessageLength {
			t.Fatalf("request %d content exceeds discord limit: %d runes", i, len([]rune(content)))
		}
		_, hasReference := req["message_reference"]
		if i == 0 && !hasReference {
			t.Fatalf("first chunk should carry the reply reference")
		}
		if i > 0 && hasReference {
			t.Fatalf("chunk %d should not carry the reply reference", i)
		}
	}
	var rebuilt strings.Builder
	for _, req := range requests {
		content, _ := req["content"].(string)
		rebuilt.WriteString(content)
	}
	// Chunk boundaries may consume the single space/newline they split on, so
	// compare word content rather than exact whitespace.
	if strings.Join(strings.Fields(rebuilt.String()), " ") != strings.Join(strings.Fields(longText), " ") {
		t.Fatalf("reassembled content does not match original text")
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
