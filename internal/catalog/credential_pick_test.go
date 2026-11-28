package catalog

import "testing"

func TestCredentialListsEveryProbedProtocol(t *testing.T) {
	supported := []string{"openai_compatible", "openai_responses"}
	if !credentialListsProtocols(supported, []string{"openai_compatible"}) {
		t.Fatal("a declared Chat protocol was rejected")
	}
	if credentialListsProtocols(supported, []string{"openai_compatible", "google_gemini_v1beta"}) {
		t.Fatal("a missing native protocol was treated as confirmed")
	}
	if credentialListsProtocols(nil, []string{"google_gemini_v1beta"}) {
		t.Fatal("an undeclared catalog row selected a Key")
	}
}
