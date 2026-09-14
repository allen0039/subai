package admin

import "testing"

func TestValidateOpenAICompatibleBaseURL(t *testing.T) {
	for _, raw := range []string{
		"https://relay.example/v1",
		"http://127.0.0.1:8317/v1/responses",
	} {
		if err := validateOpenAICompatibleBaseURL(raw); err != nil {
			t.Errorf("%q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"relay.example/v1",
		"ftp://relay.example/v1",
		"https://key@relay.example/v1",
		"https://relay.example/v1?key=secret",
	} {
		if err := validateOpenAICompatibleBaseURL(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}
