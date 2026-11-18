package controlplane

import "testing"

func TestValidateBeichenBaseURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.beichenapi.com":     "https://api.beichenapi.com",
		"https://API.BEICHENAPI.COM/v1/": "https://api.beichenapi.com",
		"https://other.example.test/v1":  "https://other.example.test/v1",
	} {
		got, err := ValidateBaseURL(input)
		if err != nil || got != want {
			t.Errorf("ValidateBaseURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"http://api.beichenapi.com",
		"https://api.beichenapi.com:8443",
		"https://api.beichenapi.com/v1/usage",
	} {
		if _, err := ValidateBaseURL(input); err == nil {
			t.Errorf("ValidateBaseURL(%q) accepted an unsupported address", input)
		}
	}
}
