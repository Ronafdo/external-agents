package aider

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
		"{\"event\":\"turn-start\",\"session_id\":\"fixture\",\"timestamp\":\"2026-09-06T10:00:01Z\",\"prompt_digest\":\"intent:create hello; prompt_sha256:abc\"}\n" +
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
	if !strings.Contains(out.String(), "prompt_sha256:abc") {
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
	initGit(t, repo)
	fakeName, fakeBody := "fake-aider.sh", "#!/bin/sh\nprintf 'hello\\n'\n"
	if runtime.GOOS == "windows" {
		fakeName, fakeBody = "fake-aider.cmd", "@echo off\r\necho hello\r\n"
	}
	fake := filepath.Join(repo, fakeName)
	if err := os.WriteFile(fake, []byte(fakeBody), 0700); err != nil {
		t.Fatal(err)
	}
	promptFile := writePromptFile(t, "hello")
	var out, errOut bytes.Buffer
	if err := Launch([]string{"--repo", repo, "--name", "demo", "--aider-bin", fake, "--message-file", promptFile}, strings.NewReader(""), &out, &errOut); err != nil {
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
	if strings.Contains(string(data), `"prompt":"hello"`) || !strings.Contains(string(data), `"prompt_digest"`) {
		t.Fatalf("journal must retain a digest rather than a raw prompt: %s", data)
	}
	if strings.Contains(string(data), `"new_files":[".entire/"]`) {
		t.Fatalf("launcher metadata must not be reported as an Aider edit: %s", data)
	}
}

func TestLauncherIsolatesSessionsAndRedactsJournal(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)

	first := writeFakeAider(t, repo, "first", "first.txt")
	second := writeFakeAider(t, repo, "second", "second.txt")
	secret := "gsk_this-must-never-reach-the-journal"
	rawPrompt := "Apply checkout validation with credential " + secret
	intent := "Validate checkout without retaining " + secret
	promptFile := writePromptFile(t, rawPrompt)
	testSecret := "gsk_test-output-must-not-persist"
	testCommand := "printf '" + testSecret + "\\n'"
	if runtime.GOOS == "windows" {
		testCommand = "echo " + testSecret
	}

	for _, tc := range []struct {
		name string
		bin  string
	}{
		{name: "first", bin: first},
		{name: "second", bin: second},
	} {
		var out, errOut bytes.Buffer
		err := Launch([]string{
			"--repo", repo,
			"--name", tc.name,
			"--aider-bin", tc.bin,
			"--message-file", promptFile,
			"--intent", intent,
			"--model", "groq/test-model",
			"--test-command", testCommand,
		}, strings.NewReader(""), &out, &errOut)
		if err != nil {
			t.Fatalf("Launch(%s): %v; stderr=%s", tc.name, err, errOut.String())
		}
	}

	firstDir := filepath.Join(sessionDir(repo), "first")
	secondDir := filepath.Join(sessionDir(repo), "second")
	for _, dir := range []string{firstDir, secondDir} {
		for _, history := range []string{"chat.history.md", "input.history", "llm.history"} {
			if _, err := os.Stat(filepath.Join(dir, history)); err != nil {
				t.Fatalf("isolated history %s: %v", history, err)
			}
		}
	}

	firstJournal, err := os.ReadFile(filepath.Join(firstDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	secondJournal, err := os.ReadFile(filepath.Join(secondDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, journal := range []string{string(firstJournal), string(secondJournal)} {
		if strings.Contains(journal, rawPrompt) || strings.Contains(journal, secret) || strings.Contains(journal, testSecret) || strings.Contains(journal, "simulated assistant response") {
			t.Fatalf("journal leaked sensitive or unredacted content: %s", journal)
		}
		for _, required := range []string{`"prompt_digest"`, `"model":"groq/test-model"`, `"test_evidence"`, `"outcome":"passed"`} {
			if !strings.Contains(journal, required) {
				t.Fatalf("journal missing %s: %s", required, journal)
			}
		}
	}
	if strings.Contains(string(firstJournal), "second.txt") || strings.Contains(string(secondJournal), "first.txt") {
		t.Fatalf("session journals must not mix changed-file evidence\nfirst=%s\nsecond=%s", firstJournal, secondJournal)
	}
	assertJournalContainsNewFile(t, firstJournal, "first.txt")
	assertJournalContainsNewFile(t, secondJournal, "second.txt")

	for _, tc := range []struct {
		name string
		bin  string
	}{
		{name: "first", bin: first},
		{name: "second", bin: second},
	} {
		args, err := os.ReadFile(filepath.Join(repo, tc.name+".args"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(args), rawPrompt) || strings.Contains(string(args), secret) {
			t.Fatalf("raw prompt was exposed in Aider arguments: %s", args)
		}
		if !strings.Contains(string(args), "--message-file") {
			t.Fatalf("launcher must pass a private message file: %s", args)
		}
	}
	for _, dir := range []string{firstDir, secondDir} {
		pending, err := filepath.Glob(filepath.Join(dir, ".aider-message-*"))
		if err != nil || len(pending) != 0 {
			t.Fatalf("raw prompt staging file was not removed from %s: %v %v", dir, pending, err)
		}
	}
}

func TestLauncherRejectsFreshDuplicateSession(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	fake := writeFakeAider(t, repo, "duplicate", "duplicate.txt")
	args := []string{"--repo", repo, "--name", "duplicate", "--aider-bin", fake, "--message-file", writePromptFile(t, "first prompt")}
	if err := Launch(args, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := Launch(args, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("fresh duplicate session must be rejected, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(sessionDir(repo), "duplicate", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"event":"session-start"`) != 1 {
		t.Fatalf("duplicate launch appended lifecycle data: %s", data)
	}
}

func TestLauncherRejectsPassthroughContinuityOverrides(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	for _, injected := range [][]string{
		{"--message", "raw prompt must not reach Aider argv"},
		{"--msg=raw prompt must not reach Aider argv"},
		{"-m", "raw prompt must not reach Aider argv"},
		{"-mraw prompt must not reach Aider argv"},
		{"--mess=raw prompt must not reach Aider argv"},
		{"--message-file=private-prompt.txt"},
		{"-f", "private-prompt.txt"},
		{"-fprivate-prompt.txt"},
		{"--chat-history-file", "outside-history.md"},
		{"--chat-history=outside-history.md"},
		{"--input-history-file=outside-input.history"},
		{"--llm-history-file=outside-llm.history"},
		{"--model", "unrecorded-model"},
		{"--restore-chat-history"},
		{"--no-restore-chat-history"},
		{"--auto-commits"},
	} {
		args := append([]string{"--repo", repo, "--name", "guarded", "--"}, injected...)
		err := Launch(args, strings.NewReader(""), ioDiscard{}, ioDiscard{})
		if err == nil || !strings.Contains(err.Error(), "owned by aider-entire") {
			t.Fatalf("passthrough %q must be rejected, got %v", injected, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sessionDir(repo), "guarded")); !os.IsNotExist(err) {
		t.Fatalf("rejected passthrough created a session directory: %v", err)
	}
	if err := Launch([]string{"--repo", repo, "--name", "raw-message", "--message", "raw credential"}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "raw --message is disabled") {
		t.Fatalf("raw launcher message must be rejected, got %v", err)
	}
}

func TestLauncherRecordsRedactedInteractiveIntent(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	fake := writeFakeAider(t, repo, "interactive", "interactive.txt")
	secret := "gsk_interactive-intent-must-not-persist"
	intent := "Investigate payment retries without retaining " + secret
	if err := Launch([]string{"--repo", repo, "--name", "interactive", "--aider-bin", fake, "--intent", intent}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join(sessionDir(repo), "interactive", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, journal, secret)
	if !strings.Contains(string(journal), `"prompt_digest":"intent:Investigate payment retries without retaining [REDACTED]"`) {
		t.Fatalf("interactive intent was not captured safely: %s", journal)
	}
	if err := Launch([]string{"--repo", repo, "--name", "missing-intent", "--aider-bin", fake}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "--intent is required") {
		t.Fatalf("interactive session without intent must fail, got %v", err)
	}
}

func TestLauncherRejectsCrossSessionResumeJournal(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	fake := writeFakeAider(t, repo, "resume", "resume.txt")
	if err := Launch([]string{"--repo", repo, "--name", "original", "--aider-bin", fake, "--message-file", writePromptFile(t, "initial")}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(sessionDir(repo), "original")
	to := filepath.Join(sessionDir(repo), "target")
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	if err := Launch([]string{"--repo", repo, "--resume", "target", "--aider-bin", fake, "--message-file", writePromptFile(t, "next")}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("cross-session resume must be rejected, got %v", err)
	}
	journal, err := os.ReadFile(filepath.Join(to, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(journal), `"event":"session-start"`) != 1 || strings.Contains(string(journal), `"session_id":"target"`) {
		t.Fatalf("cross-session resume appended mixed evidence: %s", journal)
	}
}

func TestDiffStatusClassifiesTrackedDeletion(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	path := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(path, []byte("tracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repo, "add", "tracked.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repo, "-c", "user.name=Ticket Test", "-c", "user.email=ticket@example.invalid", "commit", "-qm", "add tracked fixture").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	before := gitStatus(repo)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, _, deleted := diffStatus(before, gitStatus(repo))
	if !containsString(deleted, "tracked.txt") {
		t.Fatalf("deleted evidence missing tracked.txt: %v", deleted)
	}
}

func TestLegacyJournalIsRedactedAtEveryProtocolBoundary(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)
	secret := "gsk_legacy-secret-must-not-escape"
	rawPrompt := "Fix payment validation using " + secret
	ref := filepath.Join(sessionDir(repo), "legacy", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(ref), 0700); err != nil {
		t.Fatal(err)
	}
	legacy := journalEvent{
		Event:      "turn-start",
		SessionID:  "legacy",
		SessionRef: ref,
		Timestamp:  "2026-09-06T10:00:01Z",
		Prompt:     rawPrompt,
		Error:      "assistant response contained " + secret,
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(ref, raw, 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	input, err := json.Marshal(hookInput{SessionID: "legacy", SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	if err := Run("read-session", nil, bytes.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var got session
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, got.NativeData, rawPrompt, secret, "assistant response")
	journal, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, journal, rawPrompt, secret, "assistant response")
	if !strings.Contains(string(journal), `"prompt_digest"`) {
		t.Fatalf("legacy journal was not migrated to a prompt digest: %s", journal)
	}

	out.Reset()
	if err := Run("read-transcript", []string{"--session-ref", ref}, nil, &out); err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, out.Bytes(), rawPrompt, secret, "assistant response")

	out.Reset()
	if err := Run("extract-prompts", []string{"--session-ref", ref, "--offset", "0"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, out.Bytes(), rawPrompt, secret, "assistant response")
	if !strings.Contains(out.String(), "prompt_sha256:") {
		t.Fatalf("extract-prompts must return a digest: %s", out.String())
	}

	out.Reset()
	if err := Run("parse-hook", []string{"--hook", "turn-start"}, bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, out.Bytes(), rawPrompt, secret, "assistant response")
	if !strings.Contains(out.String(), "prompt_sha256:") {
		t.Fatalf("parse-hook must emit a digest: %s", out.String())
	}

	target := filepath.Join(sessionDir(repo), "rewritten", "events.jsonl")
	writeInput, err := json.Marshal(session{SessionID: "rewritten", SessionRef: target, NativeData: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := Run("write-session", nil, bytes.NewReader(writeInput), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, written, rawPrompt, secret, "assistant response")
}

func TestReadTranscriptRejectsPrivateAiderHistory(t *testing.T) {
	repo := t.TempDir()
	history := filepath.Join(sessionDir(repo), "private", "chat.history.md")
	if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
		t.Fatal(err)
	}
	secret := "gsk_history-must-stay-private"
	if err := os.WriteFile(history, []byte("raw prompt "+secret), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Run("read-transcript", []string{"--session-ref", history}, nil, &out)
	if err == nil || out.Len() != 0 {
		t.Fatalf("private history must not be a protocol transcript: err=%v stdout=%q", err, out.String())
	}
}

func TestNotificationPayloadSendsOnlyRedactedEvent(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "events.jsonl")
	secret := "gsk_hook-payload-must-not-escape"
	payload, err := notificationPayload(journalEvent{Event: "turn-start", SessionID: "hook", SessionRef: journal, Timestamp: "2026-09-06T10:00:01Z", Prompt: "Use " + secret})
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, payload, secret, "Use "+secret)
	if !strings.Contains(string(payload), "prompt_sha256:") {
		t.Fatalf("hook payload lacks safe prompt digest: %s", payload)
	}
}

func writeFakeAider(t *testing.T, repo, name, changedFile string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(repo, name+".cmd")
		body := "@echo off\r\necho %* > " + name + ".args\r\necho simulated assistant response " + "gsk_response-must-not-persist\r\necho changed > " + changedFile + "\r\n"
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(repo, name+".sh")
	body := "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" > " + name + ".args\nprintf 'simulated assistant response gsk_response-must-not-persist\\n'\nprintf 'changed\\n' > " + changedFile + "\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func initGit(t *testing.T, repo string) {
	t.Helper()
	if output, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
}

func writePromptFile(t *testing.T, prompt string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertJournalContainsNewFile(t *testing.T, data []byte, file string) {
	t.Helper()
	events, err := eventsFrom(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Event != "turn-end" {
			continue
		}
		for _, newFile := range event.New {
			if newFile == file {
				return
			}
		}
	}
	t.Fatalf("journal lacks new-file evidence %q: %s", file, data)
}

func assertNoPrivateContent(t *testing.T, data []byte, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(string(data), value) {
			t.Fatalf("private content %q escaped: %s", value, data)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// ioDiscard avoids importing an implementation detail into launch tests while
// still exercising the same stdout/stderr path as the CLI.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
