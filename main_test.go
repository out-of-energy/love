package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunServesACacheHitWithoutAnAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	line := `{"word":"evil","ipa":"/ˈiːvəl/","eli5":"Very, very bad.","chinese":"邪恶的"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EWH_CACHE", path)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"EVIL"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Very, very bad.") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "邪恶的") {
		t.Errorf("stdout should carry the Chinese meaning: %q", stdout.String())
	}
}

func TestRunNeedsAKeyOnACacheMiss(t *testing.T) {
	t.Setenv("EWH_CACHE", filepath.Join(t.TempDir(), "words.jsonl"))
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"serendipity"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "DEEPSEEK_API_KEY") {
		t.Errorf("stderr should explain the missing key: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("nothing should be printed on stdout, got %q", stdout.String())
	}
}

func TestRunRejectsEmptyInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "Usage: love <word>") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunReportsDamagedLinesButStillServesHits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.jsonl")
	content := "not json\n" +
		`{"word":"sign","ipa":"/saɪn/","eli5":"A sign.","chinese":"标志"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EWH_CACHE", path)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sign"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stderr.String(), "skipping invalid JSON") {
		t.Errorf("stderr should warn about the damaged line: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "A sign.") {
		t.Errorf("the healthy record should still print: %q", stdout.String())
	}
}

func TestParseArgsJoinsPhraseWords(t *testing.T) {
	var stdout, stderr bytes.Buffer
	word, done, code := parseArgs([]string{"Ice", "Cream"}, &stdout, &stderr)
	if done || code != exitOK {
		t.Fatalf("done = %v, code = %d", done, code)
	}
	if word != "ice cream" {
		t.Errorf("word = %q, want %q", word, "ice cream")
	}
}

func TestParseArgsHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		_, done, code := parseArgs(args, &stdout, &stderr)
		if !done || code != exitOK {
			t.Errorf("%v: done = %v, code = %d", args, done, code)
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Errorf("%v: help text missing", args)
		}
	}
	for _, args := range [][]string{{"--version"}, {"-V"}, {"version"}} {
		var stdout, stderr bytes.Buffer
		_, done, code := parseArgs(args, &stdout, &stderr)
		if !done || code != exitOK {
			t.Errorf("%v: done = %v, code = %d", args, done, code)
		}
		if !strings.Contains(stdout.String(), version) {
			t.Errorf("%v: version missing from %q", args, stdout.String())
		}
	}
}

func TestParseArgsRejectsUnknownFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, done, code := parseArgs([]string{"--nope"}, &stdout, &stderr)
	if !done || code != exitUsage {
		t.Fatalf("done = %v, code = %d", done, code)
	}
	if !strings.Contains(stderr.String(), "unknown option") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
