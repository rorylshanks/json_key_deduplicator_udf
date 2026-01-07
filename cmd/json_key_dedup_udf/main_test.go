package main

import (
	"bytes"
	"testing"
)

func TestProcessLineErrorsOnMalformedJSON(t *testing.T) {
	var buf bytes.Buffer
	err := processLine([]byte("{\"a\":"), &buf)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestShouldStringifyNumber(t *testing.T) {
	tests := map[string]bool{
		"0":                    false,
		"42":                   false,
		"9223372036854775807":  false,
		"9223372036854775808":  true,
		"-9223372036854775808": false,
		"-9223372036854775809": true,
		"1.25":                 false,
		"1e6":                  false,
	}

	for input, want := range tests {
		if got := shouldStringifyNumber(input); got != want {
			t.Fatalf("shouldStringifyNumber(%q) = %v, want %v", input, got, want)
		}
	}
}
