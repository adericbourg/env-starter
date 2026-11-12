package redact

import (
	"slices"
	"testing"
)

func TestLines_ofKnownValues_masksLongValuesOnly(t *testing.T) {
	// Given a long secret, a value containing it, and a short value
	lines := []string{"token is hunter2-secret-value", "full hunter2-secret-value-extended", "port 8080"}
	secrets := []string{"hunter2-secret-value", "hunter2-secret-value-extended", "8080", ""}

	// When
	got := Lines(lines, secrets)

	// Then the longest value masks fully, and the short one is left alone
	want := []string{"token is ***", "full ***", "port 8080"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLines_ofPrivateKeyBlock_masksItsBody(t *testing.T) {
	// Given
	lines := []string{
		"before",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"b3BlbnNzaC1rZXktdjEAAAAA",
		"QyNTUxOQAAACBvZ2Fy",
		"-----END OPENSSH PRIVATE KEY-----",
		"after",
	}

	// When
	got := Lines(lines, nil)

	// Then
	want := []string{
		"before",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"***",
		"***",
		"-----END OPENSSH PRIVATE KEY-----",
		"after",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLines_ofKnownTokenFormats_masksTokensAndKeepsPrefixes(t *testing.T) {
	cases := map[string]string{
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc": "Authorization: Bearer ***",
		"authorization: basic dXNlcjpwYXNz":              "authorization: basic ***",
		"curl -H bearer abc.def-ghi":                     "curl -H bearer ***",
		"PASSWORD=s3cret":                                "PASSWORD=***",
		"db_password: s3cret":                            "db_password: ***",
		"api_key=abcdef":                                 "api_key=***",
		"connecting to postgres://app:s3cret@db:5432/x":  "connecting to postgres://app:***@db:5432/x",
		"gh token ghp_abcdefghijklmnopqrstuvwxyz0123":    "gh token ***",
		"github_pat_11ABCDEFG0123456789_abcdefghij":      "***",
		"key sk-ant-api03-abcdefghijklmnopqrstuv":        "key ***",
		"aws AKIAIOSFODNN7EXAMPLE":                       "aws ***",
		"slack xoxb-1234567890-abcdefghij":               "slack ***",
		"gitlab glpat-abcdefghijklmnopqrst":              "gitlab ***",
		"google AIzaSyA1234567890abcdefghijklmnopqrstu":  "google ***",
		"server listening on :8080":                      "server listening on :8080",
	}
	for in, want := range cases {
		// When
		got := Lines([]string{in}, nil)

		// Then
		if got[0] != want {
			t.Errorf("Lines(%q) = %q, want %q", in, got[0], want)
		}
	}
}
