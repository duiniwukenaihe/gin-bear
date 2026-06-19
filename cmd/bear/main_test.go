package main

import "testing"

func TestExportedName(t *testing.T) {
	tests := map[string]string{
		"user":         "User",
		"user_profile": "UserProfile",
		"user-profile": "UserProfile",
		"API_key":      "APIKey",
	}

	for input, want := range tests {
		if got := exportedName(input); got != want {
			t.Fatalf("exportedName(%q) = %q, want %q", input, got, want)
		}
	}
}
