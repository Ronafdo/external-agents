package aider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestSessionNameMustRemainStableAcrossTheRedactionBoundary(t *testing.T) {
	for _, name := range []string{
		" leading-space",
		"trailing-space ",
		"two  spaces",
		"gsk_session-name-must-not-be-an-identity",
		strings.Repeat("a", 129),
		"C:volume-relative",
		"-looks-like-a-flag",
	} {
		if err := validateSessionName(name); err == nil {
			t.Fatalf("session name %q would change at the redaction boundary", name)
		}
	}
	if err := validateSessionName("checkout-fix_01"); err != nil {
		t.Fatalf("safe session name unexpectedly rejected: %v", err)
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

func TestResumeRejectsCrossSessionJournal(t *testing.T) {
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
	var out bytes.Buffer
	if err := Resume([]string{"--repo", repo, "--resume", "target"}, &out); err == nil || !strings.Contains(err.Error(), "does not match") || out.Len() != 0 {
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

func TestCheckpointWritesDurableRedactedContinuityBrief(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const (
		sessionID = "checkout-milestone"
		goal      = "Stabilize checkout idempotency"
		secret    = "gsk_checkpoint-must-never-persist"
		rawPrompt = "Repair checkout retries with credential " + secret
	)
	fake := writeFakeAider(t, repo, "checkpoint", "checkout.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, rawPrompt),
		"--intent", goal,
		"--model", "groq/test-model",
		"--test-command", "echo verification",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	// Native Aider histories are allowed to retain local context, but a
	// continuity brief must derive exclusively from the redacted journal.
	history := filepath.Join(sessionDir(repo), sessionID, "chat.history.md")
	if err := os.WriteFile(history, []byte(rawPrompt+"\nsimulated assistant response "+secret), 0600); err != nil {
		t.Fatal(err)
	}
	decisionFile := writeContinuityDecisionFile(t, map[string]any{
		"goal":        goal,
		"assumptions": []string{"The payment provider keeps idempotency keys for 24 hours"},
		"failures":    []string{"The first staging replay timed out with " + secret},
		"open_risks":  []string{"A legacy mobile client may retry after the retention window"},
	})

	var out bytes.Buffer
	if err := Checkpoint([]string{"--session", sessionID, "--brief-file", decisionFile}, &out); err != nil {
		t.Fatal(err)
	}
	briefPath := filepath.Join(sessionDir(repo), sessionID, "continuity-brief.json")
	brief, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("checkpoint did not persist a continuity brief: %v", err)
	}
	if !json.Valid(brief) {
		t.Fatalf("continuity brief must be JSON: %s", brief)
	}
	assertNoPrivateContent(t, brief, rawPrompt, secret, "simulated assistant response")
	assertNoPrivateContent(t, out.Bytes(), rawPrompt, secret, "simulated assistant response")

	var document map[string]json.RawMessage
	if err := json.Unmarshal(brief, &document); err != nil {
		t.Fatal(err)
	}
	assertBriefString(t, document, "session_id", sessionID)
	assertBriefString(t, document, "milestone_session_id", continuityCarrierID(brief))
	assertBriefString(t, document, "goal", goal)
	assertBriefString(t, document, "verified_outcome", "passed")
	assertBriefStrings(t, document, "assumptions", []string{"The payment provider keeps idempotency keys for 24 hours"})
	assertBriefStrings(t, document, "failures", []string{"The first staging replay timed out with [REDACTED]"})
	assertBriefStrings(t, document, "open_risks", []string{"A legacy mobile client may retry after the retention window"})

	var evidence map[string]json.RawMessage
	if raw, ok := document["evidence"]; !ok || json.Unmarshal(raw, &evidence) != nil {
		t.Fatalf("continuity brief lacks structured evidence: %s", brief)
	}
	assertBriefString(t, evidence, "model", "groq/test-model")
	assertBriefContainsString(t, evidence, "new_files", "checkout.txt")
	if raw, ok := evidence["test_evidence"]; !ok || len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		t.Fatalf("continuity brief must retain safe test evidence: %s", brief)
	}
	var integrity string
	if raw, ok := evidence["integrity_digest"]; !ok || json.Unmarshal(raw, &integrity) != nil || !strings.HasPrefix(integrity, "sha256:") {
		t.Fatalf("continuity brief must include an evidence integrity digest: %s", brief)
	}
	carrierPath := filepath.Join(sessionDir(repo), continuityCarrierID(out.Bytes()))
	if _, err := os.Stat(carrierPath); !os.IsNotExist(err) {
		t.Fatalf("a standalone checkpoint must not claim Entire capture with a carrier: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir(repo), sessionID, continuityCaptureFilename)); !os.IsNotExist(err) {
		t.Fatalf("a standalone checkpoint must not create an Entire publication receipt: %v", err)
	}
}

func TestCheckpointPublishesRedactedCarrierThroughEntireAttach(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const (
		sessionID = "notified-checkpoint"
		secret    = "gsk_carrier-must-never-contain-a-raw-prompt"
		rawPrompt = "Finish notification coverage using " + secret
	)
	fakeAider := writeFakeAider(t, repo, "notify", "notify.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fakeAider,
		"--message-file", writePromptFile(t, rawPrompt),
		"--intent", "Finish notification coverage",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := Run("install-hooks", []string{"--force"}, nil, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	writeFakeEntireAttach(t, repo)
	t.Setenv("PATH", repo+string(os.PathListSeparator)+os.Getenv("PATH"))

	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Attach the durable redacted checkpoint",
		"assumptions": []string{"The source journal is already checkpointed by normal lifecycle hooks"},
		"failures":    []string{"The raw prompt carried " + secret},
		"open_risks":  []string{},
	})
	var first bytes.Buffer
	if err := Checkpoint([]string{"--repo", repo, "--session", sessionID, "--brief-file", decision}, &first); err != nil {
		t.Fatal(err)
	}
	carrierID := continuityCarrierID(first.Bytes())
	attachArgs, err := os.ReadFile(filepath.Join(repo, "entire-attach.args"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(attachArgs)) != "session attach "+carrierID+" --agent aider" {
		t.Fatalf("checkpoint must use Entire's real carrier attach path, got %q", attachArgs)
	}
	attachCWD, err := os.ReadFile(filepath.Join(repo, "entire-attach.cwd"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(strings.TrimSpace(string(attachCWD))) != filepath.Clean(repo) {
		t.Fatalf("Entire attach must run in the source repository, got %q", attachCWD)
	}
	gitTerminalPrompt, err := os.ReadFile(filepath.Join(repo, "entire-attach.git-terminal-prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(gitTerminalPrompt)) != "0" {
		t.Fatalf("Entire attach must force the noninteractive Git path, got GIT_TERMINAL_PROMPT=%q", gitTerminalPrompt)
	}
	carrierJournal, err := os.ReadFile(filepath.Join(sessionDir(repo), carrierID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, carrierJournal, rawPrompt, secret)
	if strings.Contains(string(carrierJournal), `"event":"turn-end"`) {
		t.Fatalf("carrier must not fake a lifecycle turn-end: %s", carrierJournal)
	}
	carrierBrief, err := readContinuityBrief(repo, carrierID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(carrierBrief, first.Bytes()) {
		t.Fatalf("carrier must preserve the exact canonical brief\nwant=%q\n got=%q", first.Bytes(), carrierBrief)
	}
	carrierEvents, err := eventsFrom(carrierJournal)
	if err != nil {
		t.Fatal(err)
	}
	var milestone *journalEvent
	for i := range carrierEvents {
		if carrierEvents[i].SessionID != carrierID {
			t.Fatalf("carrier journal mixed session IDs: %+v", carrierEvents[i])
		}
		if carrierEvents[i].Event == "checkpoint-milestone" {
			milestone = &carrierEvents[i]
		}
	}
	if milestone == nil || milestone.ContinuityBrief == nil || milestone.ContinuityBrief.MilestoneSessionID != carrierID || milestone.PreviousSessionID != sessionID || milestone.ContinuityCapture == nil || milestone.ContinuityCapture.SourceSessionID != sessionID || milestone.ContinuityCapture.SourceBriefPayloadID != continuityBriefPayloadID(*milestone.ContinuityBrief) {
		t.Fatalf("carrier milestone lacks its source and brief binding: %+v", milestone)
	}
	if milestone.ContinuityCapture.SourceEvidenceID != milestone.ContinuityBrief.Evidence.IntegrityDigest {
		t.Fatalf("carrier milestone lacks its source evidence binding: %+v", milestone.ContinuityCapture)
	}
	// Entire may redact the carrier after attach. A later local checkpoint retry
	// must validate its manifest rather than reject the changed advisory prose.
	milestone.ContinuityBrief.Goal = "Attach the REDACTED checkpoint"
	carrierJournal, err = encodeJournalEvents(carrierEvents)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(sessionDir(repo), carrierID, "events.jsonl"), carrierJournal); err != nil {
		t.Fatal(err)
	}
	receiptData, err := os.ReadFile(filepath.Join(sessionDir(repo), sessionID, continuityCaptureFilename))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, receiptData, rawPrompt, secret)

	// Retrying after publication must not demand the original decision, append
	// another milestone, or invoke another capture. It is strictly an explicit,
	// idempotent read of the completed milestone.
	var retried bytes.Buffer
	if err := Checkpoint([]string{"--repo", repo, "--session", sessionID}, &retried); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), retried.Bytes()) {
		t.Fatalf("checkpoint retry must return the existing brief\nwant=%q\n got=%q", first.Bytes(), retried.Bytes())
	}
	journal, err := os.ReadFile(filepath.Join(sessionDir(repo), sessionID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(journal), `"event":"checkpoint-milestone"`) != 1 {
		t.Fatalf("checkpoint retry appended another milestone: %s", journal)
	}
	sourceEvents, err := eventsFrom(journal)
	if err != nil {
		t.Fatal(err)
	}
	turnEnds := 0
	for _, event := range sourceEvents {
		if event.Event == "turn-end" {
			turnEnds++
		}
	}
	if turnEnds != 1 {
		t.Fatalf("checkpoint must not create a synthetic source turn-end: %s", journal)
	}
	invocations, err := os.ReadFile(filepath.Join(repo, "entire-attach.invocations"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(invocations), "session attach "+carrierID+" --agent aider") != 1 {
		t.Fatalf("completed checkpoint retried Entire attachment: %s", invocations)
	}
}

func TestEntireCLIAdapterSurfacesOnlyTheSafeManualTrailer(t *testing.T) {
	repo := t.TempDir()
	writeFakeEntireAttach(t, repo)
	t.Setenv("PATH", repo+string(os.PathListSeparator)+os.Getenv("PATH"))

	carrierID := "aider-milestone-" + strings.Repeat("a", 32)
	var stderr bytes.Buffer
	if err := (entireCLIAdapter{stderr: &stderr}).Attach(context.Background(), repo, carrierID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "aider-entire --resume "+carrierID) {
		t.Fatalf("attach must expose the exact recovery session ID, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Entire-Checkpoint: abcdef123456") {
		t.Fatalf("attach must forward Entire's validated manual trailer, got %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "diagnostic that must not be forwarded") {
		t.Fatalf("attach must not forward arbitrary Entire output: %q", stderr.String())
	}
}

func TestEntireCLIAdapterTreatsNonzeroExitAsAnUnknownOutcome(t *testing.T) {
	repo := t.TempDir()
	writeFailingEntireAttach(t, repo)
	t.Setenv("PATH", repo+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := (entireCLIAdapter{}).Attach(context.Background(), repo, "aider-milestone-"+strings.Repeat("a", 32))
	if err == nil || !errors.Is(err, errAttachmentOutcomeUnknown) {
		t.Fatalf("nonzero attach must be ambiguous rather than retryable: %v", err)
	}
	if errors.Is(err, errAttachmentKnownNotPersisted) {
		t.Fatalf("a started attach must never be classified as known-unpublished: %v", err)
	}
}

func TestCheckpointDoesNotMarkDisabledEntireAsAttached(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const sessionID = "disabled-entire"
	fakeAider := writeFakeAider(t, repo, "disabled", "disabled.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fakeAider,
		"--message-file", writePromptFile(t, "record a disabled Entire checkpoint"),
		"--intent", "Record a disabled Entire checkpoint",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := Run("install-hooks", []string{"--force"}, nil, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	writeDisabledEntireAttach(t, repo)
	t.Setenv("PATH", repo+string(os.PathListSeparator)+os.Getenv("PATH"))

	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Keep the checkpoint retryable while Entire is disabled",
		"assumptions": []string{},
		"failures":    []string{},
		"open_risks":  []string{},
	})
	var first bytes.Buffer
	err := Checkpoint([]string{"--repo", repo, "--session", sessionID, "--brief-file", decision}, &first)
	if err == nil || !strings.Contains(err.Error(), "publication to Entire is pending") || first.Len() != 0 {
		t.Fatalf("disabled Entire must leave a retryable local checkpoint: err=%v stdout=%q", err, first.String())
	}
	receiptPath := filepath.Join(sessionDir(repo), sessionID, continuityCaptureFilename)
	receipt, exists, err := readContinuityCaptureReceipt(receiptPath)
	if err != nil || !exists || receipt.PublicationState != "prepared" {
		t.Fatalf("disabled Entire must not claim an attached receipt: receipt=%+v exists=%v err=%v", receipt, exists, err)
	}

	writeFakeEntireAttach(t, repo)
	var retried bytes.Buffer
	if err := Checkpoint([]string{"--repo", repo, "--session", sessionID}, &retried); err != nil {
		t.Fatalf("checkpoint must succeed after Entire is re-enabled: %v", err)
	}
	if retried.Len() == 0 {
		t.Fatal("successful retry must return the stored Continuity Brief")
	}
	receipt, exists, err = readContinuityCaptureReceipt(receiptPath)
	if err != nil || !exists || receipt.PublicationState != "attached" {
		t.Fatalf("re-enabled Entire must mark the receipt attached: receipt=%+v exists=%v err=%v", receipt, exists, err)
	}
}

func TestCheckpointLeavesCarrierPendingUntilAnExplicitPublicationRetry(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const sessionID = "pending-publication"
	fakeAider := writeFakeAider(t, repo, "pending", "pending.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fakeAider,
		"--message-file", writePromptFile(t, "record evidence before publication"),
		"--intent", "Record evidence before publication",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := Run("install-hooks", []string{"--force"}, nil, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Preserve a durable recovery point",
		"assumptions": []string{},
		"failures":    []string{},
		"open_risks":  []string{},
	})
	publisher := &recordingCheckpointPublisher{
		err: fmt.Errorf("%w: simulated Entire outage", errAttachmentKnownNotPersisted),
		verify: func(root, carrierID string) error {
			_, err := readContinuityBrief(root, carrierID)
			return err
		},
	}
	var first bytes.Buffer
	err := checkpointWithPublisher([]string{"--repo", repo, "--session", sessionID, "--brief-file", decision}, &first, publisher)
	if err == nil || !strings.Contains(err.Error(), "publication to Entire is pending") || first.Len() != 0 {
		t.Fatalf("failed attach must leave a pending local milestone without stdout: err=%v stdout=%q", err, first.String())
	}
	brief, err := readContinuityBrief(repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	carrierID := continuityCarrierID(brief)
	if len(publisher.calls) != 1 || publisher.calls[0] != carrierID {
		t.Fatalf("expected one explicit publication attempt for %q, got %+v", carrierID, publisher.calls)
	}
	if _, err := readContinuityBrief(repo, carrierID); err != nil {
		t.Fatalf("pending publication must retain a valid carrier: %v", err)
	}
	receiptPath := filepath.Join(sessionDir(repo), sessionID, continuityCaptureFilename)
	receipt, exists, err := readContinuityCaptureReceipt(receiptPath)
	if err != nil || !exists || receipt.PublicationState != "prepared" {
		t.Fatalf("failed attach must leave prepared retry state: receipt=%+v exists=%v err=%v", receipt, exists, err)
	}

	publisher.err = nil
	var retried bytes.Buffer
	if err := checkpointWithPublisher([]string{"--repo", repo, "--session", sessionID}, &retried, publisher); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retried.Bytes(), brief) {
		t.Fatalf("explicit retry must return the original brief\nwant=%q\n got=%q", brief, retried.Bytes())
	}
	if len(publisher.calls) != 2 || publisher.calls[1] != carrierID {
		t.Fatalf("only the explicit retry may make a second attach attempt: %+v", publisher.calls)
	}
	receipt, exists, err = readContinuityCaptureReceipt(receiptPath)
	if err != nil || !exists || receipt.PublicationState != "attached" {
		t.Fatalf("successful explicit retry must mark the carrier attached: receipt=%+v exists=%v err=%v", receipt, exists, err)
	}

	// A failed process can leave the receipt torn after Entire has already
	// accepted the carrier. The next explicit checkpoint must reconcile against
	// Entire metadata, never blindly create a second checkpoint.
	if err := writePrivateFile(receiptPath, []byte(`{"publication_state":`)); err != nil {
		t.Fatal(err)
	}
	publisher.calls = nil
	publisher.findCalls = nil
	var repaired bytes.Buffer
	if err := checkpointWithPublisher([]string{"--repo", repo, "--session", sessionID}, &repaired, publisher); err != nil {
		t.Fatalf("malformed receipt must reconcile through an explicit retry: %v", err)
	}
	if !bytes.Equal(repaired.Bytes(), brief) {
		t.Fatalf("receipt reconciliation must return the original brief\nwant=%q\n got=%q", brief, repaired.Bytes())
	}
	if len(publisher.calls) != 0 || len(publisher.findCalls) != 1 || publisher.findCalls[0] != carrierID {
		t.Fatalf("receipt reconciliation must query instead of reattaching %q: attach=%+v find=%+v", carrierID, publisher.calls, publisher.findCalls)
	}
	receipt, exists, err = readContinuityCaptureReceipt(receiptPath)
	if err != nil || !exists || receipt.PublicationState != "attached" {
		t.Fatalf("receipt reconciliation must restore attached state: receipt=%+v exists=%v err=%v", receipt, exists, err)
	}

	// An empty branch-scoped checkpoint query cannot prove absence. Without a
	// positive match, a damaged receipt must stop rather than double-attach.
	if err := writePrivateFile(receiptPath, []byte(`{"publication_state":`)); err != nil {
		t.Fatal(err)
	}
	publisher.attached = false
	publisher.calls = nil
	publisher.findCalls = nil
	var unknown bytes.Buffer
	err = checkpointWithPublisher([]string{"--repo", repo, "--session", sessionID}, &unknown, publisher)
	if err == nil || !strings.Contains(err.Error(), "publication state is unknown") || unknown.Len() != 0 {
		t.Fatalf("unproven publication must fail closed without stdout: err=%v stdout=%q", err, unknown.String())
	}
	if len(publisher.calls) != 0 || len(publisher.findCalls) != 1 || publisher.findCalls[0] != carrierID {
		t.Fatalf("unproven publication must reconcile without reattaching: attach=%+v find=%+v", publisher.calls, publisher.findCalls)
	}
}

func TestCheckpointPublishesLegacyBriefWithoutEmbeddedCarrierID(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const sessionID = "legacy-carrier-id"
	fakeAider := writeFakeAider(t, repo, "legacy", "legacy.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fakeAider,
		"--message-file", writePromptFile(t, "prepare a legacy checkpoint"),
		"--intent", "Prepare a legacy checkpoint",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Publish an existing schema-one checkpoint",
		"assumptions": []string{},
		"failures":    []string{},
		"open_risks":  []string{},
	})
	if err := Checkpoint([]string{"--repo", repo, "--session", sessionID, "--brief-file", decision}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	// Simulate a Brief persisted by the first Ticket 3 implementation, before
	// `milestone_session_id` existed. Its source journal must stay authoritative
	// and cannot be rewritten merely to add a convenience recovery field.
	briefPath := filepath.Join(sessionDir(repo), sessionID, continuityBriefFilename)
	briefData, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatal(err)
	}
	var legacyBrief continuityBrief
	if err := decodeStrictJSON(briefData, &legacyBrief); err != nil {
		t.Fatal(err)
	}
	legacyBrief.MilestoneSessionID = ""
	legacyData, err := json.Marshal(legacyBrief)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(briefPath, legacyData); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(sessionDir(repo), sessionID, "events.jsonl")
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventsFrom(journal)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		if events[i].Event == "checkpoint-milestone" && events[i].ContinuityBrief != nil {
			events[i].ContinuityBrief.MilestoneSessionID = ""
		}
	}
	legacyJournal, err := encodeJournalEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(journalPath, legacyJournal); err != nil {
		t.Fatal(err)
	}
	if err := Run("install-hooks", []string{"--force"}, nil, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	publisher := &recordingCheckpointPublisher{}
	var published bytes.Buffer
	if err := checkpointWithPublisher([]string{"--repo", repo, "--session", sessionID}, &published, publisher); err != nil {
		t.Fatalf("legacy checkpoint must remain publishable: %v", err)
	}
	if !bytes.Equal(published.Bytes(), legacyData) {
		t.Fatalf("legacy retry must preserve its source Brief\nwant=%q\n got=%q", legacyData, published.Bytes())
	}
	carrierID := continuityCarrierID(legacyData)
	if len(publisher.calls) != 1 || publisher.calls[0] != carrierID {
		t.Fatalf("legacy retry must attach its derived carrier, got %+v", publisher.calls)
	}
	var resumed bytes.Buffer
	if err := Resume([]string{"--repo", repo, "--resume", carrierID}, &resumed); err != nil {
		t.Fatalf("legacy carrier must remain recoverable: %v", err)
	}
	if !bytes.Equal(resumed.Bytes(), legacyData) {
		t.Fatalf("legacy carrier must return original Brief\nwant=%q\n got=%q", legacyData, resumed.Bytes())
	}
}

func TestResumeReturnsExactBriefWithoutRestartingAiderOrMutatingSession(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const (
		sessionID = "read-only-resume"
		secret    = "gsk_resume-must-never-persist"
		rawPrompt = "Create the initial implementation using " + secret
	)
	fake := writeCountingAider(t, repo, "aider", "resume-target.txt")
	t.Setenv("PATH", repo+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, rawPrompt),
		"--intent", "Create initial implementation",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir(repo), sessionID, "chat.history.md"), []byte(rawPrompt+"\nsimulated assistant response "+secret), 0600); err != nil {
		t.Fatal(err)
	}
	decisionFile := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Create initial implementation",
		"assumptions": []string{"The local repository is authoritative"},
		"failures":    []string{},
		"open_risks":  []string{"A developer must explicitly choose the next task"},
	})
	if err := Checkpoint([]string{"--session", sessionID, "--brief-file", decisionFile}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	journalPath := filepath.Join(sessionDir(repo), sessionID, "events.jsonl")
	briefPath := filepath.Join(sessionDir(repo), sessionID, "continuity-brief.json")
	codePath := filepath.Join(repo, "resume-target.txt")
	invocationsPath := filepath.Join(repo, "aider-invocations.log")
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	briefBefore, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatal(err)
	}
	codeBefore, err := os.ReadFile(codePath)
	if err != nil {
		t.Fatal(err)
	}
	invocationsBefore, err := os.ReadFile(invocationsPath)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Resume([]string{"--repo", repo, "--resume", sessionID}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), briefBefore) {
		t.Fatalf("resume must return the persisted, machine-readable brief byte-for-byte\nwant=%q\n got=%q", briefBefore, out.Bytes())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("resume output must remain parseable JSON: %s", out.Bytes())
	}
	assertNoPrivateContent(t, briefBefore, rawPrompt, secret, "simulated assistant response")
	assertNoPrivateContent(t, out.Bytes(), rawPrompt, secret, "simulated assistant response")
	assertFileUnchanged(t, journalPath, journalBefore)
	assertFileUnchanged(t, briefPath, briefBefore)
	assertFileUnchanged(t, codePath, codeBefore)
	assertFileUnchanged(t, invocationsPath, invocationsBefore)
}

func TestResumeRejectsTamperedJournalEvidence(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const sessionID = "integrity-checked"
	fake := writeFakeAider(t, repo, "integrity", "integrity.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, "make integrity evidence"),
		"--intent", "Make integrity evidence",
		"--model", "groq/verified-model",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Recover only evidence that still matches the checkpoint",
		"assumptions": []string{},
		"failures":    []string{},
		"open_risks":  []string{},
	})
	if err := Checkpoint([]string{"--repo", repo, "--session", sessionID, "--brief-file", decision}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(sessionDir(repo), sessionID, "events.jsonl")
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventsFrom(journal)
	if err != nil {
		t.Fatal(err)
	}
	tampered := false
	for i := range events {
		if events[i].Event == "turn-end" {
			events[i].Model = "groq/tampered-model"
			tampered = true
			break
		}
	}
	if !tampered {
		t.Fatal("fixture lacks a turn-end evidence record")
	}
	tamperedJournal, err := encodeJournalEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(journalPath, tamperedJournal); err != nil {
		t.Fatal(err)
	}
	journalBeforeResume, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Resume([]string{"--repo", repo, "--resume", sessionID}, &out); err == nil || !strings.Contains(err.Error(), "integrity digest") || out.Len() != 0 {
		t.Fatalf("tampered evidence must fail closed: err=%v stdout=%q", err, out.String())
	}
	assertFileUnchanged(t, journalPath, journalBeforeResume)
}

func TestResumeValidatesCarrierBindingAndAcceptsEntireRedactedProse(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const sessionID = "carrier-integrity"
	fake := writeFakeAider(t, repo, "carrier-integrity", "carrier-integrity.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, "create carrier integrity evidence"),
		"--intent", "Create carrier integrity evidence",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if err := Run("install-hooks", []string{"--force"}, nil, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Carry Qn7mL2vX9kR4bY8pT6cH1wZ5fD3sG0jE through a safe handoff",
		"assumptions": []string{},
		"failures":    []string{},
		"open_risks":  []string{},
	})
	var checkpoint bytes.Buffer
	if err := checkpointWithPublisher([]string{"--repo", repo, "--session", sessionID, "--brief-file", decision}, &checkpoint, &recordingCheckpointPublisher{}); err != nil {
		t.Fatal(err)
	}
	carrierID := continuityCarrierID(checkpoint.Bytes())
	carrierPath := filepath.Join(sessionDir(repo), carrierID, "events.jsonl")
	data, err := os.ReadFile(carrierPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventsFrom(data)
	if err != nil {
		t.Fatal(err)
	}
	tampered := false
	for i := range events {
		if events[i].Event == "checkpoint-milestone" && events[i].ContinuityCapture != nil {
			events[i].ContinuityCapture.SourceBriefPayloadID = contentDigest("different brief")
			tampered = true
		}
	}
	if !tampered {
		t.Fatal("fixture lacks a continuity carrier milestone")
	}
	tamperedData, err := encodeJournalEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(carrierPath, tamperedData); err != nil {
		t.Fatal(err)
	}
	beforeResume, err := os.ReadFile(carrierPath)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Resume([]string{"--repo", repo, "--resume", carrierID}, &out); err == nil || !strings.Contains(err.Error(), "binding manifest") || out.Len() != 0 {
		t.Fatalf("tampered carrier must fail closed: err=%v stdout=%q", err, out.String())
	}
	assertFileUnchanged(t, carrierPath, beforeResume)

	// Entire applies another redaction pass while persisting the carrier. That
	// pass can replace high-entropy decision prose, but it must not invalidate
	// the redaction-invariant carrier binding manifest.
	redactedEvents, err := eventsFrom(data)
	if err != nil {
		t.Fatal(err)
	}
	redacted := false
	for i := range redactedEvents {
		if redactedEvents[i].Event != "checkpoint-milestone" || redactedEvents[i].ContinuityBrief == nil || redactedEvents[i].ContinuityCapture == nil {
			continue
		}
		redactedEvents[i].ContinuityBrief.Goal = "Carry REDACTED through a safe handoff"
		redacted = true
	}
	if !redacted {
		t.Fatal("fixture lacks a redaction-compatible continuity carrier milestone")
	}
	redactedData, err := encodeJournalEvents(redactedEvents)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(carrierPath, redactedData); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Resume([]string{"--repo", repo, "--resume", carrierID}, &out); err != nil {
		t.Fatalf("Entire-redacted carrier prose must remain resumable: %v", err)
	}
	var resumed continuityBrief
	if err := decodeStrictJSON(out.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Goal != "Carry REDACTED through a safe handoff" {
		t.Fatalf("resume must present Entire-redacted advisory prose, got %q", resumed.Goal)
	}
}

func TestCheckpointAndResumeFailClosedForInvalidOrMismatchedSessionPaths(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)
	decisionFile := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Safely hand off implementation state",
		"assumptions": []string{},
		"failures":    []string{},
		"open_risks":  []string{},
	})

	for _, tc := range []struct {
		name string
		call func(*bytes.Buffer) error
	}{
		{
			name: "checkpoint rejects a missing session",
			call: func(out *bytes.Buffer) error {
				return Checkpoint([]string{"--session", "missing", "--brief-file", decisionFile}, out)
			},
		},
		{
			name: "resume rejects a missing session",
			call: func(out *bytes.Buffer) error {
				return Resume([]string{"--repo", repo, "--resume", "missing"}, out)
			},
		},
		{
			name: "path traversal session id is rejected",
			call: func(out *bytes.Buffer) error {
				return Resume([]string{"--repo", repo, "--resume", ".."}, out)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tc.call(&out); err == nil || out.Len() != 0 {
				t.Fatalf("must fail closed without partial output: err=%v stdout=%q", err, out.String())
			}
		})
	}

	// A renamed directory is not the Aider Session named by its journal. A
	// checkpoint must not attach a brief to that mismatched path.
	fake := writeFakeAider(t, repo, "mismatch", "mismatch.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", "source-session",
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, "original work"),
		"--intent", "Original work",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(sessionDir(repo), "source-session")
	to := filepath.Join(sessionDir(repo), "mismatched-session")
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(to, "events.jsonl")
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointOut bytes.Buffer
	if err := Checkpoint([]string{"--session", "mismatched-session", "--brief-file", decisionFile}, &checkpointOut); err == nil || checkpointOut.Len() != 0 {
		t.Fatalf("mismatched journal path must fail closed: err=%v stdout=%q", err, checkpointOut.String())
	}
	assertFileUnchanged(t, journalPath, journalBefore)
	if _, err := os.Stat(filepath.Join(to, "continuity-brief.json")); !os.IsNotExist(err) {
		t.Fatalf("mismatched checkpoint must not create a brief: %v", err)
	}

	// A canonical session without its generated brief cannot be resumed.
	validID := "no-brief-yet"
	if err := Launch([]string{
		"--repo", repo,
		"--name", validID,
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, "work without a checkpoint"),
		"--intent", "Work without a checkpoint",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	validJournal := filepath.Join(sessionDir(repo), validID, "events.jsonl")
	validJournalBefore, err := os.ReadFile(validJournal)
	if err != nil {
		t.Fatal(err)
	}
	missingDecision := filepath.Join(t.TempDir(), "missing-decision.json")
	var missingDecisionOut bytes.Buffer
	if err := Checkpoint([]string{"--session", validID, "--brief-file", missingDecision}, &missingDecisionOut); err == nil || missingDecisionOut.Len() != 0 {
		t.Fatalf("checkpoint with a missing decision file must fail closed: err=%v stdout=%q", err, missingDecisionOut.String())
	}
	if _, err := os.Stat(filepath.Join(sessionDir(repo), validID, "continuity-brief.json")); !os.IsNotExist(err) {
		t.Fatalf("missing decision file must not create a brief: %v", err)
	}
	var resumeOut bytes.Buffer
	if err := Resume([]string{"--repo", repo, "--resume", validID}, &resumeOut); err == nil || resumeOut.Len() != 0 {
		t.Fatalf("resume without a brief must fail closed: err=%v stdout=%q", err, resumeOut.String())
	}
	assertFileUnchanged(t, validJournal, validJournalBefore)

	// A present but malformed brief is not a recoverable checkpoint. It must
	// not be printed or used as a route to implicitly restart the session.
	malformedPath := filepath.Join(sessionDir(repo), validID, "continuity-brief.json")
	if err := os.WriteFile(malformedPath, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	var malformedOut bytes.Buffer
	if err := Resume([]string{"--repo", repo, "--resume", validID}, &malformedOut); err == nil || malformedOut.Len() != 0 {
		t.Fatalf("malformed brief must fail closed: err=%v stdout=%q", err, malformedOut.String())
	}
	assertFileUnchanged(t, validJournal, validJournalBefore)
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

func writeContinuityDecisionFile(t *testing.T, decision map[string]any) string {
	t.Helper()
	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "continuity-decision.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertBriefString(t *testing.T, document map[string]json.RawMessage, key, want string) {
	t.Helper()
	raw, ok := document[key]
	if !ok {
		t.Fatalf("continuity brief lacks %q", key)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil || got != want {
		t.Fatalf("continuity brief %q = %q, want %q (err=%v)", key, got, want, err)
	}
}

func assertBriefStrings(t *testing.T, document map[string]json.RawMessage, key string, want []string) {
	t.Helper()
	raw, ok := document[key]
	if !ok {
		t.Fatalf("continuity brief lacks %q", key)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != len(want) {
		t.Fatalf("continuity brief %q = %v, want %v (err=%v)", key, got, want, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("continuity brief %q = %v, want %v", key, got, want)
		}
	}
}

func assertBriefContainsString(t *testing.T, document map[string]json.RawMessage, key, want string) {
	t.Helper()
	raw, ok := document[key]
	if !ok {
		t.Fatalf("continuity brief evidence lacks %q", key)
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("decode evidence %q: %v", key, err)
	}
	if !containsString(values, want) {
		t.Fatalf("continuity brief evidence %q = %v, want %q", key, values, want)
	}
}

func assertFileUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s after resume: %v", path, err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("resume mutated %s\nbefore=%q\n after=%q", path, before, after)
	}
}

func writeCountingAider(t *testing.T, repo, name, changedFile string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(repo, name+".cmd")
		body := "@echo off\r\necho invoked>> aider-invocations.log\r\necho %* > aider.args\r\necho changed > " + changedFile + "\r\n"
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(repo, name)
	body := "#!/usr/bin/env sh\nprintf 'invoked\\n' >> aider-invocations.log\nprintf '%s\\n' \"$@\" > aider.args\nprintf 'changed\\n' > " + changedFile + "\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
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

func writeFakeEntireAttach(t *testing.T, repo string) {
	t.Helper()
	argsPath := filepath.Join(repo, "entire-attach.args")
	invocationsPath := filepath.Join(repo, "entire-attach.invocations")
	cwdPath := filepath.Join(repo, "entire-attach.cwd")
	promptPath := filepath.Join(repo, "entire-attach.git-terminal-prompt")
	if runtime.GOOS == "windows" {
		path := filepath.Join(repo, "entire.cmd")
		body := "@echo off\r\n" +
			"if /I not \"%1\"==\"session\" exit /b 1\r\n" +
			"if /I not \"%2\"==\"attach\" exit /b 1\r\n" +
			"if /I not \"%4\"==\"--agent\" exit /b 1\r\n" +
			"if /I not \"%5\"==\"aider\" exit /b 1\r\n" +
			"if not \"%6\"==\"\" exit /b 1\r\n" +
			"echo %* > \"" + argsPath + "\"\r\n" +
			"echo %* >> \"" + invocationsPath + "\"\r\n" +
			"echo %CD% > \"" + cwdPath + "\"\r\n" +
			"echo %GIT_TERMINAL_PROMPT% > \"" + promptPath + "\"\r\n" +
			"echo Attached session %3\r\n" +
			"echo Entire attach diagnostic that must not be forwarded\r\n" +
			"echo   Entire-Checkpoint: abcdef123456\r\n" +
			"exit /b 0\r\n"
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(repo, "entire")
	body := "#!/usr/bin/env sh\n" +
		"[ \"$#\" -eq 5 ] && [ \"$1\" = session ] && [ \"$2\" = attach ] && [ \"$4\" = --agent ] && [ \"$5\" = aider ] || exit 1\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(argsPath) + "\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(invocationsPath) + "\n" +
		"pwd > " + shellQuote(cwdPath) + "\n" +
		"printf '%s\\n' \"$GIT_TERMINAL_PROMPT\" > " + shellQuote(promptPath) + "\n" +
		"printf 'Attached session %s\\n' \"$3\"\n" +
		"printf '%s\\n' 'Entire attach diagnostic that must not be forwarded'\n" +
		"printf '%s\\n' '  Entire-Checkpoint: abcdef123456'\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeFailingEntireAttach(t *testing.T, repo string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(repo, "entire.cmd"), []byte("@echo off\r\nexit /b 7\r\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(repo, "entire"), []byte("#!/usr/bin/env sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeDisabledEntireAttach(t *testing.T, repo string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(repo, "entire.cmd"), []byte("@echo off\r\necho Entire is disabled. Run `entire enable` to re-enable.\r\nexit /b 0\r\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(repo, "entire"), []byte("#!/usr/bin/env sh\nprintf '%s\\n' 'Entire is disabled. Run `entire enable` to re-enable.'\n"), 0700); err != nil {
		t.Fatal(err)
	}
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

type recordingCheckpointPublisher struct {
	calls     []string
	findCalls []string
	err       error
	findErr   error
	attached  bool
	verify    func(root, sessionID string) error
}

func (publisher *recordingCheckpointPublisher) Attach(_ context.Context, root, sessionID string) error {
	publisher.calls = append(publisher.calls, sessionID)
	if publisher.verify != nil {
		if err := publisher.verify(root, sessionID); err != nil {
			return err
		}
	}
	if publisher.err == nil {
		publisher.attached = true
	}
	return publisher.err
}

func (publisher *recordingCheckpointPublisher) FindAttached(_ context.Context, _ string, sessionID string) (bool, error) {
	publisher.findCalls = append(publisher.findCalls, sessionID)
	if publisher.findErr != nil {
		return false, publisher.findErr
	}
	return publisher.attached, nil
}
