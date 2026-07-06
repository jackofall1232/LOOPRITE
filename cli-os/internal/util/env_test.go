package util

import (
	"reflect"
	"testing"
)

func TestScrubSecretEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"LOOPRITE_MASTER_KEY=supersecretbase64",
		"HOME=/home/x",
		"LOOPRITE_SETUP_SECRET=abc123",
		"LOOPRITE_MASTER_KEYSTORE=notthesamevar", // prefix collision must NOT be scrubbed
	}
	got := ScrubSecretEnv(in)
	want := []string{"PATH=/usr/bin", "HOME=/home/x", "LOOPRITE_MASTER_KEYSTORE=notthesamevar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScrubSecretEnv = %v, want %v", got, want)
	}
}

func TestScrubSecretEnvEmpty(t *testing.T) {
	if got := ScrubSecretEnv(nil); got != nil {
		t.Fatalf("nil in must give nil out, got %v", got)
	}
	if got := ScrubSecretEnv([]string{}); len(got) != 0 {
		t.Fatalf("empty in must give empty out, got %v", got)
	}
}

func TestScrubSecretEnvNoSecretsUnchangedContent(t *testing.T) {
	in := []string{"PATH=/usr/bin", "HOME=/home/x"}
	got := ScrubSecretEnv(in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("ScrubSecretEnv with no secrets present = %v, want %v", got, in)
	}
}
