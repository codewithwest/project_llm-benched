package api

import "testing"

func TestReplayTargetURLUsesCapturedEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		endpoint string
		want     string
	}{
		{name: "ollama", provider: "http://localhost:11434", endpoint: "/api/generate", want: "http://localhost:11434/api/generate"},
		{name: "openai", provider: "https://engine.example/v1", endpoint: "/v1/chat/completions", want: "https://engine.example/v1/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := replayTargetURL(tt.provider, tt.endpoint)
			if err != nil {
				t.Fatalf("replayTargetURL failed: %v", err)
			}
			if got != tt.want {
				t.Fatalf("replayTargetURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
