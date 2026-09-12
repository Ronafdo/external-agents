package aider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchResumeOnlyRetrievesBrief(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const sessionID = "resume-preview"
	fake := writeFakeAider(t, repo, "preview", "preview.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sessionID,
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, "create the preview fixture"),
		"--intent", "Create a preview fixture",
		"--test-command", "echo verified",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Recover the preview fixture safely",
		"assumptions": []string{"The canonical journal is available"},
		"failures":    []string{},
		"open_risks":  []string{"A developer must choose the next instruction"},
	})
	if err := Checkpoint([]string{"--repo", repo, "--session", sessionID, "--brief-file", decision}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	journalPath := filepath.Join(sessionDir(repo), sessionID, "events.jsonl")
	briefPath := filepath.Join(sessionDir(repo), sessionID, continuityBriefFilename)
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	briefBefore, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary bytes.Buffer
	if err := Run("extract-summary", []string{"--session-ref", journalPath}, nil, &summary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.String(), `"has_summary":true`) || !strings.Contains(summary.String(), "Recover the preview fixture safely") {
		t.Fatalf("checkpoint milestone was not exposed through Entire's summary seam: %s", summary.String())
	}
	var out, errOut bytes.Buffer
	if err := Launch([]string{"--repo", repo, "--resume", sessionID}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("resume preview: %v; stderr=%s", err, errOut.String())
	}
	if !bytes.Equal(out.Bytes(), briefBefore) {
		t.Fatalf("--resume output must be the exact stored brief\nwant=%q\n got=%q", briefBefore, out.Bytes())
	}
	assertFileUnchanged(t, journalPath, journalBefore)
	assertFileUnchanged(t, briefPath, briefBefore)
}

func TestContinueFromStartsFreshLinkedSessionOnlyWithExplicitInstruction(t *testing.T) {
	repo := t.TempDir()
	initGit(t, repo)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	const sourceID = "source-checkpoint"
	sourceAider := writeFakeAider(t, repo, "source", "source.txt")
	if err := Launch([]string{
		"--repo", repo,
		"--name", sourceID,
		"--aider-bin", sourceAider,
		"--message-file", writePromptFile(t, "finish the source work"),
		"--intent", "Finish source work",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Carry verified source decisions forward",
		"assumptions": []string{"A new session gets fresh histories"},
		"failures":    []string{},
		"open_risks":  []string{"The next change still needs review"},
	})
	if err := Checkpoint([]string{"--repo", repo, "--session", sourceID, "--brief-file", decision}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	sourceJournalPath := filepath.Join(sessionDir(repo), sourceID, "events.jsonl")
	sourceJournal, err := os.ReadFile(sourceJournalPath)
	if err != nil {
		t.Fatal(err)
	}

	followupAider := writeFakeAider(t, repo, "followup", "followup.txt")
	err = Launch([]string{
		"--repo", repo,
		"--continue-from", sourceID,
		"--name", "followup-session",
		"--aider-bin", followupAider,
		"--intent", "Apply the developer's next decision",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "explicit --message-file") {
		t.Fatalf("continuation without a next instruction must fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir(repo), "followup-session")); !os.IsNotExist(err) {
		t.Fatalf("unapproved continuation created a session: %v", err)
	}

	secret := "gsk_followup-never-in-argv"
	nextInstruction := "Handle the revised checkout requirement with " + secret
	if err := Launch([]string{
		"--repo", repo,
		"--continue-from", sourceID,
		"--name", "followup-session",
		"--aider-bin", followupAider,
		"--message-file", writePromptFile(t, nextInstruction),
		"--intent", "Apply a revised checkout decision",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	assertFileUnchanged(t, sourceJournalPath, sourceJournal)

	followupJournalPath := filepath.Join(sessionDir(repo), "followup-session", "events.jsonl")
	followupJournal, err := os.ReadFile(followupJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, followupJournal, nextInstruction, secret)
	events, err := eventsFrom(followupJournal)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].PreviousSessionID != sourceID || events[0].ContinuityBrief == nil {
		t.Fatalf("fresh session lacks its safe continuity link: %+v", events)
	}
	args, err := os.ReadFile(filepath.Join(repo, "followup.args"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, args, nextInstruction, secret)
}

func TestResumeRecoversCheckpointRestoredByEntireWithoutSidecar(t *testing.T) {
	sourceRepo := t.TempDir()
	initGit(t, sourceRepo)
	t.Setenv("ENTIRE_REPO_ROOT", sourceRepo)

	const (
		sessionID = "restored-checkpoint"
		secret    = "gsk_restored-checkpoint-must-not-persist"
		rawPrompt = "Prepare a durable checkout handoff with " + secret
	)
	fake := writeFakeAider(t, sourceRepo, "restore-source", "source.txt")
	if err := Launch([]string{
		"--repo", sourceRepo,
		"--name", sessionID,
		"--aider-bin", fake,
		"--message-file", writePromptFile(t, rawPrompt),
		"--intent", "Prepare a durable checkout handoff",
		"--test-command", "echo verified",
	}, strings.NewReader(""), ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	decision := writeContinuityDecisionFile(t, map[string]any{
		"goal":        "Resume Qn7mL2vX9kR4bY8pT6cH1wZ5fD3sG0jE work from verified evidence",
		"assumptions": []string{"Entire restored the canonical Aider transcript"},
		"failures":    []string{},
		"open_risks":  []string{"A developer must provide the next instruction"},
	})
	if err := Run("install-hooks", []string{"--force"}, nil, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	publisher := &recordingCheckpointPublisher{}
	var checkpoint bytes.Buffer
	if err := checkpointWithPublisher([]string{"--repo", sourceRepo, "--session", sessionID, "--brief-file", decision}, &checkpoint, publisher); err != nil {
		t.Fatal(err)
	}
	carrierID := continuityCarrierID(checkpoint.Bytes())
	if len(publisher.calls) != 1 || publisher.calls[0] != carrierID {
		t.Fatalf("checkpoint did not publish its carrier: %+v", publisher.calls)
	}

	sourceRef := filepath.Join(sessionDir(sourceRepo), carrierID, "events.jsonl")
	sourceBrief, err := os.ReadFile(filepath.Join(sessionDir(sourceRepo), sessionID, continuityBriefFilename))
	if err != nil {
		t.Fatal(err)
	}
	var exported bytes.Buffer
	hookData, err := json.Marshal(hookInput{SessionID: carrierID, SessionRef: sourceRef})
	if err != nil {
		t.Fatal(err)
	}
	if err := Run("read-session", nil, bytes.NewReader(hookData), &exported); err != nil {
		t.Fatal(err)
	}
	var snapshot session
	if err := json.Unmarshal(exported.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, snapshot.NativeData, rawPrompt, secret, "simulated assistant response")
	// Entire redacts the native data again at attach time. Model that concrete
	// entropy-redaction result before its write-session restore boundary: the
	// carrier binding remains stable while the decision prose becomes safer.
	carrierEvents, err := eventsFrom(snapshot.NativeData)
	if err != nil {
		t.Fatal(err)
	}
	redacted := false
	for i := range carrierEvents {
		if carrierEvents[i].Event != "checkpoint-milestone" || carrierEvents[i].ContinuityBrief == nil {
			continue
		}
		carrierEvents[i].ContinuityBrief.Goal = "Resume REDACTED work from verified evidence"
		redacted = true
	}
	if !redacted {
		t.Fatal("exported carrier lacks a Continuity Brief to redact")
	}
	snapshot.NativeData, err = encodeJournalEvents(carrierEvents)
	if err != nil {
		t.Fatal(err)
	}

	restoredRepo := t.TempDir()
	initGit(t, restoredRepo)
	t.Setenv("ENTIRE_REPO_ROOT", restoredRepo)
	restoredRef := filepath.Join(sessionDir(restoredRepo), carrierID, "events.jsonl")
	restoreData, err := json.Marshal(session{SessionID: carrierID, SessionRef: restoredRef, NativeData: snapshot.NativeData})
	if err != nil {
		t.Fatal(err)
	}
	if err := Run("write-session", nil, bytes.NewReader(restoreData), ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir(restoredRepo), carrierID, continuityBriefFilename)); !os.IsNotExist(err) {
		t.Fatalf("Entire restore must not need a launcher-local brief sidecar: %v", err)
	}
	restoredJournal, err := os.ReadFile(restoredRef)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateContent(t, restoredJournal, rawPrompt, secret, "simulated assistant response")
	events, err := eventsFrom(restoredJournal)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.SessionRef != restoredRef || event.RepoPath != restoredRepo {
			t.Fatalf("restored event has stale checkout metadata: %+v", event)
		}
	}

	var out bytes.Buffer
	if err := Resume([]string{"--repo", restoredRepo, "--resume", carrierID}, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(out.Bytes(), sourceBrief) {
		t.Fatalf("restored Brief should reflect Entire's additional redaction\nsource=%q\nrestored=%q", sourceBrief, out.Bytes())
	}
	assertNoPrivateContent(t, out.Bytes(), rawPrompt, secret, "simulated assistant response")
	var resumed continuityBrief
	if err := decodeStrictJSON(out.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Goal != "Resume REDACTED work from verified evidence" {
		t.Fatalf("restored resume must preserve Entire-redacted prose, got %q", resumed.Goal)
	}

	// A restored checkout contains only the carrier. Source IDs intentionally do
	// not trigger a directory scan, which prevents an arbitrary local carrier
	// from being substituted for the original Aider Session.
	out.Reset()
	if err := Resume([]string{"--repo", restoredRepo, "--resume", sessionID}, &out); err == nil || out.Len() != 0 {
		t.Fatalf("restored source ID must not resolve through an untrusted carrier scan: err=%v stdout=%q", err, out.String())
	}
}

func TestExtractSummaryRejectsAnArbitraryFileReference(t *testing.T) {
	var out bytes.Buffer
	privatePath := filepath.Join(t.TempDir(), "private-aider-history.jsonl")
	if err := Run("extract-summary", []string{"--session-ref", privatePath}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"has_summary":false`) || strings.Contains(out.String(), privatePath) {
		t.Fatalf("noncanonical reference must expose no summary: %s", out.String())
	}
}
