package aider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProtocolSessionFixture(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("ENTIRE_REPO_ROOT", repo)
	ref := filepath.Join(sessionDir(repo), "fixture", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(ref), 0750); err != nil {
		t.Fatal(err)
	}
	fixture := "{\"event\":\"session-start\",\"session_id\":\"fixture\",\"timestamp\":\"2026-09-06T10:00:00Z\"}\n" +
		"{\"event\":\"turn-start\",\"session_id\":\"fixture\",\"timestamp\":\"2026-09-06T10:00:01Z\",\"prompt\":\"create hello\"}\n" +
		"{\"event\":\"turn-end\",\"session_id\":\"fixture\",\"timestamp\":\"2026-09-06T10:00:02Z\",\"modified_files\":[\"hello.txt\"]}\n"
	if err := os.WriteFile(ref, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	inputData, err := json.Marshal(hookInput{SessionID: "fixture", SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.NewReader(inputData)
	if err := Run("read-session", nil, input, &out); err != nil {
		t.Fatal(err)
	}
	var got session
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "fixture" || len(got.ModifiedFiles) != 1 || got.ModifiedFiles[0] != "hello.txt" {
		t.Fatalf("unexpected session: %+v", got)
	}
	out.Reset()
	if err := Run("extract-prompts", []string{"--session-ref", ref, "--offset", "0"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "create hello") {
		t.Fatalf("prompt missing: %s", out.String())
	}
}

func TestInvalidProtocolDoesNotWriteStdout(t *testing.T) {
	var out bytes.Buffer
	err := Run("parse-hook", []string{"--hook", "turn-start"}, strings.NewReader("not-json"), &out)
	if err == nil || out.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, out.String())
	}
}

func TestLauncherCreatesInspectableSession(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0750); err != nil {
		t.Fatal(err)
	}
	fakeName, fakeBody := "fake-aider.sh", "#!/bin/sh\nprintf 'hello\\n'\n"
	if runtime.GOOS == "windows" {
		fakeName, fakeBody = "fake-aider.cmd", "@echo off\r\necho hello\r\n"
	}
	fake := filepath.Join(repo, fakeName)
	if err := os.WriteFile(fake, []byte(fakeBody), 0700); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Launch([]string{"--repo", repo, "--name", "demo", "--aider-bin", fake, "--message", "hello"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(sessionDir(repo), "demo", "events.jsonl")
	data, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"session-start"`) || !strings.Contains(string(data), `"turn-end"`) {
		t.Fatalf("missing lifecycle events: %s", data)
	}
	if strings.Contains(string(data), `"new_files":[".entire/"]`) {
		t.Fatalf("launcher metadata must not be reported as an Aider edit: %s", data)
	}
}
