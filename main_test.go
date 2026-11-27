package main

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
)

func TestCompletionAndModelsURL(t *testing.T) {
	tests := []struct {
		input      string
		completion string
		models     string
	}{
		{"https://la-api.vxvxvx.top/v1", "https://la-api.vxvxvx.top/v1/chat/completions", "https://la-api.vxvxvx.top/v1/models"},
		{"https://example.com/v1/chat/completions", "https://example.com/v1/chat/completions", "https://example.com/v1/models"},
	}
	for _, test := range tests {
		got, err := completionURL(test.input)
		if err != nil || got != test.completion {
			t.Fatalf("completionURL(%q) = %q, %v", test.input, got, err)
		}
		got, err = modelsURL(test.input)
		if err != nil || got != test.models {
			t.Fatalf("modelsURL(%q) = %q, %v", test.input, got, err)
		}
	}
}

func TestOfficialFrontendRequestBecomesStandardOpenAIRequest(t *testing.T) {
	const frontendBody = `{"model":"deepseek-flash","system":"该助手为DeepSeek，由深度求索公司创造。","messages":[{"role":"latest_reminder","content":"2026-11-27,星期五,,Web,中文"},{"role":"user","content":"你是什么模型"}],"thinking":{"type":"disabled"},"safety_check":{"level":"high"},"tools":[{"type":"deepseek_read_url","name":"deepseek_read_url"}],"title":{"enabled":true},"stream":true}`
	in, err := decodeChatRequest(strings.NewReader(frontendBody))
	if err != nil {
		t.Fatalf("official frontend request was rejected: %v", err)
	}
	payload, lastUser, err := buildOpenAIRequest(in, llmConfig{Model: "gpt-5.3-codex", Temperature: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	if lastUser != "你是什么模型" || payload.Model != "gpt-5.3-codex" || len(payload.Messages) != 1 {
		t.Fatalf("unexpected upstream payload: %#v, last user %q", payload, lastUser)
	}
	if payload.Messages[0].Role != "user" || payload.Messages[0].Content != "你是什么模型" {
		t.Fatalf("unexpected upstream messages: %#v", payload.Messages)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"deepseek-flash", "latest_reminder", "system", "thinking", "safety_check", "tools", "title"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("upstream payload leaked frontend-only field %q: %s", forbidden, raw)
		}
	}
}

func TestSecretRoundTripAndPasswordHash(t *testing.T) {
	a := &app{key: sha256.Sum256([]byte("test-secret"))}
	enc, err := a.encrypt("sk-private")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := a.decrypt(enc)
	if err != nil || plain != "sk-private" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
	if a.hashPassword("one") == a.hashPassword("two") {
		t.Fatal("different passwords produced the same hash")
	}
}
