package main

import (
	"crypto/sha256"
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
