// Package aider implements the Entire protocol around sessions created by the
// aider-entire launcher. The launcher owns the durable boundary because Aider
// does not expose stable lifecycle hooks.
package aider

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const protocolVersion = 1

const (
	continuityBriefVersion  = 1
	continuityBriefFilename = "continuity-brief.json"
)

type capabilities struct {
	Hooks              bool `json:"hooks"`
	TranscriptAnalyzer bool `json:"transcript_analyzer"`
	TranscriptPreparer bool `json:"transcript_preparer"`
	TokenCalculator    bool `json:"token_calculator"`
	TextGenerator      bool `json:"text_generator"`
	HookResponseWriter bool `json:"hook_response_writer"`
	SubagentAware      bool `json:"subagent_aware_extractor"`
}

type info struct {
	ProtocolVersion int          `json:"protocol_version"`
	Name            string       `json:"name"`
	Type            string       `json:"type"`
	Description     string       `json:"description"`
	IsPreview       bool         `json:"is_preview"`
	ProtectedDirs   []string     `json:"protected_dirs"`
	HookNames       []string     `json:"hook_names"`
	Capabilities    capabilities `json:"capabilities"`
}

type hookInput struct {
	HookType   string `json:"hook_type"`
	SessionID  string `json:"session_id"`
	SessionRef string `json:"session_ref"`
	Timestamp  string `json:"timestamp"`
}

type session struct {
	SessionID     string   `json:"session_id"`
	AgentName     string   `json:"agent_name"`
	RepoPath      string   `json:"repo_path"`
	SessionRef    string   `json:"session_ref"`
	StartTime     string   `json:"start_time"`
	NativeData    []byte   `json:"native_data"`
	ModifiedFiles []string `json:"modified_files"`
	NewFiles      []string `json:"new_files"`
	DeletedFiles  []string `json:"deleted_files"`
}

// journalEvent is intentionally small and append-only. It is both the
// launcher journal and the parse-hook payload format.
type journalEvent struct {
	Event             string `json:"event"`
	SessionID         string `json:"session_id"`
	PreviousSessionID string `json:"previous_session_id,omitempty"`
	SessionRef        string `json:"session_ref,omitempty"`
	RepoPath          string `json:"repo_path,omitempty"`
	Timestamp         string `json:"timestamp"`
	// Prompt is accepted only to migrate legacy journals. New journal entries
	// must use PromptDigest, which is deliberately non-reversible.
	Prompt          string           `json:"prompt,omitempty"`
	PromptDigest    string           `json:"prompt_digest,omitempty"`
	Model           string           `json:"model,omitempty"`
	Modified        []string         `json:"modified_files,omitempty"`
	New             []string         `json:"new_files,omitempty"`
	Deleted         []string         `json:"deleted_files,omitempty"`
	TestEvidence    []testEvidence   `json:"test_evidence,omitempty"`
	Outcome         string           `json:"outcome,omitempty"`
	DurationMS      int64            `json:"duration_ms,omitempty"`
	ExitCode        *int             `json:"exit_code,omitempty"`
	FailureKind     string           `json:"failure_kind,omitempty"`
	ContinuityBrief *continuityBrief `json:"continuity_brief,omitempty"`
	// Error is accepted only to migrate legacy journals. It is never emitted
	// because tool errors can contain prompt, response, or credential data.
	Error string `json:"error,omitempty"`
}

// testEvidence deliberately records only a non-reversible command fingerprint
// and outcome. Test output can contain project data or credentials and belongs
// in the developer's terminal, not checkpoint context.
type testEvidence struct {
	CommandDigest string `json:"command_digest"`
	Outcome       string `json:"outcome"`
	ExitCode      int    `json:"exit_code"`
	DurationMS    int64  `json:"duration_ms"`
}

// continuityDecision is developer-authored, curated checkpoint input. It is
// read from a file so potentially sensitive text never has to travel in argv.
// The launcher stores only its redacted form in the Continuity Brief.
type continuityDecision struct {
	Goal        string   `json:"goal"`
	Assumptions []string `json:"assumptions"`
	Failures    []string `json:"failures"`
	OpenRisks   []string `json:"open_risks"`
}

// continuityEvidence is deliberately made entirely from canonical journal
// data. It never includes raw Aider history, command text, test output, model
// responses, or credentials.
type continuityEvidence struct {
	IntegrityDigest string         `json:"integrity_digest"`
	Model           string         `json:"model,omitempty"`
	ModifiedFiles   []string       `json:"modified_files,omitempty"`
	NewFiles        []string       `json:"new_files,omitempty"`
	DeletedFiles    []string       `json:"deleted_files,omitempty"`
	TestEvidence    []testEvidence `json:"test_evidence,omitempty"`
}

// continuityBrief is the durable, checkpoint-safe handoff artifact. A copy is
// stored beside the isolated session for direct recovery and embedded in the
// append-only journal milestone so Entire can retain it with the session.
type continuityBrief struct {
	SchemaVersion   int                `json:"schema_version"`
	SessionID       string             `json:"session_id"`
	CreatedAt       string             `json:"created_at"`
	Goal            string             `json:"goal"`
	VerifiedOutcome string             `json:"verified_outcome"`
	Evidence        continuityEvidence `json:"evidence"`
	Assumptions     []string           `json:"assumptions"`
	Failures        []string           `json:"failures"`
	OpenRisks       []string           `json:"open_risks"`
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func Run(command string, args []string, stdin io.Reader, stdout io.Writer) error {
	switch command {
	case "info":
		return writeJSON(stdout, info{protocolVersion, "aider", "Aider", "Aider session integration via aider-entire", true, []string{".entire/aider-sessions"}, []string{"session-start", "turn-start", "turn-end", "session-end"}, capabilities{Hooks: true, TranscriptAnalyzer: true}})
	case "detect":
		_, err := exec.LookPath("aider")
		return writeJSON(stdout, map[string]bool{"present": err == nil})
	case "get-session-id":
		var input hookInput
		if err := decode(stdin, &input); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"session_id": input.SessionID})
	case "get-session-dir":
		fs := flags(command, args)
		repo := fs.String("repo-path", "", "repo")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"session_dir": sessionDir(repoPath(*repo))})
	case "resolve-session-file":
		fs := flags(command, args)
		dir := fs.String("session-dir", "", "dir")
		id := fs.String("session-id", "", "id")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := validateSessionName(*id); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"session_file": filepath.Join(*dir, *id, "events.jsonl")})
	case "read-session":
		return readSession(stdin, stdout)
	case "write-session":
		return writeSession(stdin)
	case "read-transcript":
		fs := flags(command, args)
		ref := fs.String("session-ref", "", "ref")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return readTranscript(stdout, *ref)
	case "chunk-transcript":
		return chunk(args, stdin, stdout)
	case "reassemble-transcript":
		return reassemble(stdin, stdout)
	case "format-resume-command":
		fs := flags(command, args)
		id := fs.String("session-id", "", "id")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := validateSessionName(*id); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"command": "aider-entire --resume " + shellQuote(*id)})
	case "parse-hook":
		return parseHook(args, stdin, stdout)
	case "install-hooks":
		return installHooks(args, stdout)
	case "uninstall-hooks":
		return os.Remove(markerPath(repoPath("")))
	case "are-hooks-installed":
		_, err := os.Stat(markerPath(repoPath("")))
		return writeJSON(stdout, map[string]bool{"installed": err == nil})
	case "get-transcript-position":
		return transcriptPosition(args, stdout)
	case "extract-modified-files":
		return extractFiles(args, stdout)
	case "extract-prompts":
		return extractPrompts(args, stdout)
	case "extract-summary":
		return extractSummary(args, stdout)
	default:
		return fmt.Errorf("unknown subcommand: %s", command)
	}
}

func flags(name string, args []string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
func decode(r io.Reader, v any) error    { return json.NewDecoder(r).Decode(v) }
func writeJSON(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func repoPath(path string) string {
	if path != "" {
		return path
	}
	if v := os.Getenv("ENTIRE_REPO_ROOT"); v != "" {
		return v
	}
	v, _ := os.Getwd()
	return v
}
func sessionDir(repo string) string { return filepath.Join(repo, ".entire", "aider-sessions") }
func markerPath(repo string) string { return filepath.Join(repo, ".entire", "aider-entire.json") }

func validateSessionName(name string) error {
	if name == "" {
		return errors.New("session-id is required")
	}
	if name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return errors.New("session name must not contain path separators")
	}
	return nil
}

// readTranscript makes the journal's privacy invariant defensive: old
// launcher versions could have written a raw prompt, so canonical Aider JSONL
// is normalized before it leaves the external-agent boundary.
func readTranscript(w io.Writer, path string) error {
	if path == "" {
		return errors.New("session-ref is required")
	}
	if err := validateCanonicalJournalRef(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	safe, journal, _, err := sanitizeNativeData(data)
	if err != nil {
		return err
	}
	if journal && !bytes.Equal(data, safe) {
		if err := writePrivateFile(path, safe); err != nil {
			return err
		}
	}
	_, err = w.Write(safe)
	return err
}

func validateCanonicalJournalRef(ref string) error {
	if filepath.Base(ref) != "events.jsonl" {
		return errors.New("Aider transcript must be the canonical events.jsonl journal")
	}
	sessionID := filepath.Base(filepath.Dir(ref))
	if err := validateSessionName(sessionID); err != nil {
		return err
	}
	if filepath.Base(filepath.Dir(filepath.Dir(ref))) != "aider-sessions" {
		return errors.New("Aider transcript is outside the session journal directory")
	}
	return nil
}

func readSession(stdin io.Reader, stdout io.Writer) error {
	var input hookInput
	if err := decode(stdin, &input); err != nil {
		return err
	}
	ref := input.SessionRef
	if ref == "" && input.SessionID != "" {
		ref = filepath.Join(sessionDir(repoPath("")), input.SessionID, "events.jsonl")
	}
	if ref == "" {
		return errors.New("session_ref or session_id is required")
	}
	if input.SessionID != "" {
		if err := validateSessionName(input.SessionID); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(ref)
	if err != nil {
		return err
	}
	native, journal, events, err := sanitizeNativeData(raw)
	if err != nil {
		return err
	}
	if !journal {
		events, err = eventsFrom(native)
		if err != nil {
			return err
		}
	}
	if journal && !bytes.Equal(raw, native) {
		if err := writePrivateFile(ref, native); err != nil {
			return err
		}
	}
	journalID, err := singleSessionID(events)
	if err != nil {
		return err
	}
	id := input.SessionID
	if journalID != "" {
		if id != "" && id != journalID {
			return fmt.Errorf("session id %q does not match journal session %q", id, journalID)
		}
		id = journalID
		if err := validateSessionRef(ref, repoPath(""), id); err != nil {
			return err
		}
	}
	start := time.Now().UTC().Format(time.RFC3339)
	repo := repoPath("")
	var modified, created, deleted []string
	for _, e := range events {
		if id == "" {
			id = e.SessionID
		}
		if e.RepoPath != "" {
			repo = e.RepoPath
		}
		if e.Event == "session-start" {
			start = e.Timestamp
		}
		modified = append(modified, e.Modified...)
		created = append(created, e.New...)
		deleted = append(deleted, e.Deleted...)
	}
	return writeJSON(stdout, session{id, "aider", repo, ref, start, native, unique(modified), unique(created), unique(deleted)})
}

func writeSession(stdin io.Reader) error {
	var s session
	if err := decode(stdin, &s); err != nil {
		return err
	}
	if s.SessionRef == "" {
		return errors.New("session_ref is required")
	}
	safe, canonical, events, err := sanitizeNativeData(s.NativeData)
	if err != nil {
		return err
	}
	// A checkpoint restore can land in a fresh checkout. For canonical Aider
	// journals, rewrite only the location metadata to the new repo-scoped
	// session reference; the embedded Continuity Brief remains the source of
	// truth and raw Aider histories are never reconstructed.
	if canonical {
		id, err := singleSessionID(events)
		if err != nil {
			return err
		}
		root, err := filepath.Abs(repoPath(""))
		if err != nil {
			return err
		}
		// Keep the protocol's generic opaque/session-copy behavior intact when
		// Entire gives this agent a different target Session ID. A real
		// checkpoint restore preserves its ID; only then is it safe and useful
		// to rewrite workstation-local location metadata for the new checkout.
		if id != "" && (s.SessionID == "" || s.SessionID == id) && validateSessionRef(s.SessionRef, root, id) == nil {
			targetRef, err := filepath.Abs(s.SessionRef)
			if err != nil {
				return err
			}
			for i := range events {
				events[i].SessionRef = targetRef
				events[i].RepoPath = root
			}
			safe, err = encodeJournalEvents(events)
			if err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.SessionRef), 0700); err != nil {
		return err
	}
	return writePrivateFile(s.SessionRef, safe)
}

func encodeJournalEvents(events []journalEvent) ([]byte, error) {
	lines := make([][]byte, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(sanitizeJournalEvent(event))
		if err != nil {
			return nil, err
		}
		lines = append(lines, encoded)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return append(bytes.Join(lines, []byte("\n")), '\n'), nil
}
func chunk(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flags("chunk-transcript", args)
	max := fs.Int("max-size", 0, "max")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *max <= 0 {
		return errors.New("max-size must be positive")
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	chunks := make([][]byte, 0)
	for len(data) > 0 {
		n := *max
		if n > len(data) {
			n = len(data)
		}
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	return writeJSON(stdout, map[string]any{"chunks": chunks})
}
func reassemble(stdin io.Reader, stdout io.Writer) error {
	var input struct {
		Chunks [][]byte `json:"chunks"`
	}
	if err := decode(stdin, &input); err != nil {
		return err
	}
	for _, c := range input.Chunks {
		if _, err := stdout.Write(c); err != nil {
			return err
		}
	}
	return nil
}
func parseHook(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flags("parse-hook", args)
	hook := fs.String("hook", "", "hook")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return writeJSON(stdout, nil)
	}
	var e journalEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("invalid aider journal event: %w", err)
	}
	if e.SessionID == "" {
		return errors.New("aider journal event missing session_id")
	}
	e = sanitizeJournalEvent(e)
	typ := map[string]int{"session-start": 1, "turn-start": 2, "turn-end": 3, "session-end": 5}[*hook]
	if typ == 0 {
		typ = map[string]int{"session-start": 1, "turn-start": 2, "turn-end": 3, "session-end": 5}[e.Event]
	}
	if typ == 0 {
		return writeJSON(stdout, nil)
	}
	payload := map[string]any{"type": typ, "session_id": e.SessionID, "session_ref": e.SessionRef, "model": e.Model, "timestamp": e.Timestamp}
	if e.PromptDigest != "" {
		// The protocol's `prompt` is optional. For Aider, it carries the
		// non-reversible digest rather than the raw developer instruction.
		payload["prompt"] = e.PromptDigest
		payload["metadata"] = map[string]string{"prompt_digest": e.PromptDigest}
	}
	return writeJSON(stdout, payload)
}
func installHooks(args []string, stdout io.Writer) error {
	fs := flags("install-hooks", args)
	_ = fs.Bool("local-dev", false, "local")
	force := fs.Bool("force", false, "force")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := markerPath(repoPath(""))
	if _, err := os.Stat(path); err == nil && !*force {
		return writeJSON(stdout, map[string]int{"hooks_installed": 0})
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte("{\"launcher\":\"aider-entire\",\"version\":1}\n"), 0600); err != nil {
		return err
	}
	return writeJSON(stdout, map[string]int{"hooks_installed": 4})
}
func transcriptPosition(args []string, stdout io.Writer) error {
	fs := flags("get-transcript-position", args)
	path := fs.String("path", "", "path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info, err := os.Stat(*path)
	if os.IsNotExist(err) {
		return writeJSON(stdout, map[string]int{"position": 0})
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]int64{"position": info.Size()})
}
func extractFiles(args []string, stdout io.Writer) error {
	fs := flags("extract-modified-files", args)
	path := fs.String("path", "", "path")
	off := fs.Int64("offset", 0, "offset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	if *off < 0 {
		return errors.New("offset is outside transcript")
	}
	if *off > int64(len(data)) {
		return writeJSON(stdout, map[string]any{"files": []string{}, "current_position": len(data)})
	}
	events, err := eventsFrom(data[*off:])
	if err != nil {
		return err
	}
	if _, err := singleSessionID(events); err != nil {
		return err
	}
	var files []string
	for _, e := range events {
		files = append(files, e.Modified...)
	}
	return writeJSON(stdout, map[string]any{"files": unique(files), "current_position": len(data)})
}
func extractPrompts(args []string, stdout io.Writer) error {
	fs := flags("extract-prompts", args)
	path := fs.String("session-ref", "", "ref")
	off := fs.Int64("offset", 0, "offset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	if *off < 0 {
		return errors.New("offset is outside transcript")
	}
	if *off > int64(len(data)) {
		return writeJSON(stdout, map[string]any{"prompts": []string{}})
	}
	events, err := eventsFrom(data[*off:])
	if err != nil {
		return err
	}
	if _, err := singleSessionID(events); err != nil {
		return err
	}
	var prompts []string
	for _, e := range events {
		if e.Event == "turn-start" && e.PromptDigest != "" {
			prompts = append(prompts, e.PromptDigest)
		}
	}
	return writeJSON(stdout, map[string]any{"prompts": prompts})
}

// extractSummary exposes the latest explicit checkpoint milestone through the
// protocol's existing summary seam. This lets Entire include the same redacted
// handoff context it stores in the canonical transcript.
func extractSummary(args []string, stdout io.Writer) error {
	fs := flags("extract-summary", args)
	ref := fs.String("session-ref", "", "ref")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ref == "" {
		return errors.New("session-ref is required")
	}
	if err := validateCanonicalJournalRef(*ref); err != nil {
		return writeJSON(stdout, map[string]any{"summary": "", "has_summary": false})
	}
	sessionID := filepath.Base(filepath.Dir(*ref))
	root, err := filepath.Abs(repoPath(""))
	if err != nil {
		return writeJSON(stdout, map[string]any{"summary": "", "has_summary": false})
	}
	if err := validateSessionRef(*ref, root, sessionID); err != nil {
		return writeJSON(stdout, map[string]any{"summary": "", "has_summary": false})
	}
	data, err := readContinuityBrief(root, sessionID)
	if err != nil {
		return writeJSON(stdout, map[string]any{"summary": "", "has_summary": false})
	}
	var brief continuityBrief
	if err := decodeStrictJSON(data, &brief); err != nil {
		return writeJSON(stdout, map[string]any{"summary": "", "has_summary": false})
	}
	// The canonical brief is already a compact, deterministic JSON document.
	// Returning it whole (rather than a lossy prose digest) ensures Entire's
	// checkpoint context retains the assumptions, failures, open risks, and
	// detailed Evidence Records required to make recovery actionable.
	return writeJSON(stdout, map[string]any{"summary": string(data), "has_summary": true})
}

var (
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:gsk|sk|rk|xai)[_-][A-Za-z0-9_-]+`),
		regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|password)\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`),
		regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~-]+`),
	}
	structuredPromptDigest = regexp.MustCompile(`^(?:intent:[^;\r\n]{1,192}|(?:intent:[^;\r\n]{1,192}; )?prompt_sha256:[A-Za-z0-9]{3,64}(?:; chars:[0-9]+)?)$`)
	sha256Digest           = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

func contentDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum)
}

// promptDigest preserves only an optional, deliberately supplied intent label
// after redaction. The developer's actual prompt is represented by a hash and
// its character count, never by recoverable prompt text.
func promptDigest(prompt, intent string) string {
	parts := make([]string, 0, 3)
	if safeIntent := redactText(intent, 192); safeIntent != "" {
		parts = append(parts, "intent:"+safeIntent)
	}
	if prompt != "" {
		parts = append(parts, "prompt_"+contentDigest(prompt), fmt.Sprintf("chars:%d", len([]rune(prompt))))
	}
	return strings.Join(parts, "; ")
}

func redactText(value string, limit int) string {
	value = strings.TrimSpace(value)
	for _, pattern := range secretPatterns {
		value = pattern.ReplaceAllString(value, "[REDACTED]")
	}
	value = strings.Join(strings.Fields(value), " ")
	if limit > 0 && len([]rune(value)) > limit {
		value = string([]rune(value)[:limit]) + "…"
	}
	return value
}

func safePromptDigest(value string) string {
	value = redactText(value, 240)
	if value == "" {
		return ""
	}
	if structuredPromptDigest.MatchString(value) {
		return value
	}
	return "prompt_" + contentDigest(value)
}

func safeOutcome(value string) string {
	switch value {
	case "passed", "failed", "skipped", "running", "unverified":
		return value
	case "":
		return ""
	default:
		return "unknown"
	}
}

func safeFailureKind(value string) string {
	switch value {
	case "aider-exit-nonzero", "aider-launch-error", "verification-failed", "legacy-error-redacted":
		return value
	case "":
		return ""
	default:
		return "redacted-error"
	}
}

func safeVerifiedOutcome(value string) string {
	switch value {
	case "passed", "failed", "unverified":
		return value
	default:
		return "unverified"
	}
}

func redactDecision(value string) string {
	return redactText(value, 240)
}

func sanitizeContinuityBrief(brief continuityBrief) continuityBrief {
	brief.SchemaVersion = continuityBriefVersion
	brief.SessionID = redactText(brief.SessionID, 128)
	brief.CreatedAt = redactText(brief.CreatedAt, 64)
	brief.Goal = redactDecision(brief.Goal)
	brief.VerifiedOutcome = safeVerifiedOutcome(brief.VerifiedOutcome)
	brief.Evidence.Model = redactText(brief.Evidence.Model, 192)
	if !sha256Digest.MatchString(brief.Evidence.IntegrityDigest) {
		brief.Evidence.IntegrityDigest = contentDigest(brief.Evidence.IntegrityDigest)
	}
	brief.Evidence.ModifiedFiles = unique(brief.Evidence.ModifiedFiles)
	brief.Evidence.NewFiles = unique(brief.Evidence.NewFiles)
	brief.Evidence.DeletedFiles = unique(brief.Evidence.DeletedFiles)
	for i := range brief.Evidence.TestEvidence {
		test := &brief.Evidence.TestEvidence[i]
		if !sha256Digest.MatchString(test.CommandDigest) {
			test.CommandDigest = contentDigest(test.CommandDigest)
		}
		test.Outcome = safeOutcome(test.Outcome)
	}
	brief.Assumptions = sanitizeDecisions(brief.Assumptions)
	brief.Failures = sanitizeDecisions(brief.Failures)
	brief.OpenRisks = sanitizeDecisions(brief.OpenRisks)
	return brief
}

func sanitizeDecisions(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if safe := redactDecision(value); safe != "" {
			result = append(result, safe)
		}
	}
	return unique(result)
}

// sanitizeJournalEvent is the one-way boundary from native Aider data to
// Entire's durable continuity context and hook payloads.
func sanitizeJournalEvent(event journalEvent) journalEvent {
	if event.PromptDigest != "" {
		event.PromptDigest = safePromptDigest(event.PromptDigest)
	} else if event.Prompt != "" {
		event.PromptDigest = promptDigest(event.Prompt, "")
	}
	event.Prompt = ""
	event.Model = redactText(event.Model, 192)
	event.Outcome = safeOutcome(event.Outcome)
	if event.Error != "" && event.FailureKind == "" {
		event.FailureKind = "legacy-error-redacted"
	}
	event.Error = ""
	event.FailureKind = safeFailureKind(event.FailureKind)
	event.PreviousSessionID = redactText(event.PreviousSessionID, 128)
	for i := range event.TestEvidence {
		test := &event.TestEvidence[i]
		if !sha256Digest.MatchString(test.CommandDigest) {
			test.CommandDigest = contentDigest(test.CommandDigest)
		}
		test.Outcome = safeOutcome(test.Outcome)
	}
	if event.ContinuityBrief != nil {
		brief := sanitizeContinuityBrief(*event.ContinuityBrief)
		event.ContinuityBrief = &brief
	}
	return event
}

// sanitizeNativeData recognizes the launcher's canonical JSONL format. Opaque
// protocol data is intentionally left byte-for-byte intact so generic Entire
// session round-trips keep working; canonical Aider data is always rewritten
// without raw prompt, response, or error text.
func sanitizeNativeData(data []byte) ([]byte, bool, []journalEvent, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 4*1024*1024)
	var events []journalEvent
	var lines [][]byte
	seenEvent := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			if seenEvent || bytes.Contains(bytes.ToLower(line), []byte(`"prompt"`)) {
				return nil, false, nil, fmt.Errorf("parse journal: %w", err)
			}
			return data, false, nil, nil
		}
		if _, ok := fields["event"]; !ok {
			if seenEvent {
				return nil, false, nil, errors.New("journal contains mixed event and opaque data")
			}
			if _, hasPrompt := fields["prompt"]; hasPrompt {
				return nil, false, nil, errors.New("opaque native data must not contain raw prompts")
			}
			return data, false, nil, nil
		}
		seenEvent = true
		var event journalEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, false, nil, fmt.Errorf("parse journal: %w", err)
		}
		if event.Event == "" || event.SessionID == "" {
			return nil, false, nil, errors.New("aider journal event requires event and session_id")
		}
		event = sanitizeJournalEvent(event)
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, false, nil, err
		}
		lines = append(lines, encoded)
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, nil, err
	}
	if !seenEvent {
		return data, false, nil, nil
	}
	if _, err := singleSessionID(events); err != nil {
		return nil, false, nil, err
	}
	return append(bytes.Join(lines, []byte("\n")), '\n'), true, events, nil
}

func eventsFrom(data []byte) ([]journalEvent, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 4*1024*1024)
	var out []journalEvent
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var event journalEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse journal: %w", err)
		}
		out = append(out, sanitizeJournalEvent(event))
	}
	return out, scanner.Err()
}

func singleSessionID(events []journalEvent) (string, error) {
	var id string
	for _, event := range events {
		if event.Event == "" {
			continue
		}
		if event.SessionID == "" {
			return "", errors.New("aider journal event missing session_id")
		}
		if id == "" {
			id = event.SessionID
		} else if id != event.SessionID {
			return "", errors.New("journal contains more than one Aider Session")
		}
	}
	return id, nil
}

func validateSessionRef(ref, root, id string) error {
	want, err := filepath.Abs(filepath.Join(sessionDir(root), id, "events.jsonl"))
	if err != nil {
		return err
	}
	got, err := filepath.Abs(ref)
	if err != nil {
		return err
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		return errors.New("session_ref does not belong to the declared Aider Session")
	}
	return nil
}

func writePrivateFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to write Aider session data through a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// Checkpoint records an explicit, decision-rich Continuity Brief. The brief
// is written locally for recovery and embedded in the append-only journal so
// Entire captures it as part of the external-agent session transcript.
func Checkpoint(args []string, stdout io.Writer) error {
	fs := flags("checkpoint", args)
	repo := fs.String("repo", "", "repository path")
	sessionID := fs.String("session", "", "Aider Session name")
	briefFile := fs.String("brief-file", "", "JSON file containing curated checkpoint decisions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateSessionName(*sessionID); err != nil {
		return err
	}
	root, err := filepath.Abs(repoPath(*repo))
	if err != nil {
		return err
	}
	release, err := acquireEvidenceLock(root, "checkpoint-"+*sessionID)
	if err != nil {
		return err
	}
	defer release()
	dir, journal, _, events, err := readCanonicalSession(root, *sessionID)
	if err != nil {
		return err
	}
	briefPath := filepath.Join(dir, continuityBriefFilename)
	if _, err := os.Lstat(briefPath); err == nil {
		// A prior attempt can have attached the local brief and milestone but
		// failed while notifying Entire. Validate that durable state, then retry
		// only the supported checkpoint notification. This never launches Aider,
		// changes source files, or appends a duplicate milestone.
		briefData, err := readContinuityBrief(root, *sessionID)
		if err != nil {
			return err
		}
		var brief continuityBrief
		if err := decodeStrictJSON(briefData, &brief); err != nil {
			return err
		}
		if err := notifyCheckpointMilestone(root, journal, brief); err != nil {
			return err
		}
		_, err = stdout.Write(briefData)
		return err
	} else if !os.IsNotExist(err) {
		return err
	}
	if *briefFile == "" {
		return errors.New("--brief-file is required when creating a Continuity Brief")
	}
	decision, err := readContinuityDecision(*briefFile)
	if err != nil {
		return err
	}
	brief, err := buildContinuityBrief(*sessionID, events, decision)
	if err != nil {
		return err
	}
	briefData, err := json.Marshal(brief)
	if err != nil {
		return err
	}
	if err := writePrivateFile(briefPath, briefData); err != nil {
		return err
	}
	milestone := journalEvent{
		Event:           "checkpoint-milestone",
		SessionID:       *sessionID,
		SessionRef:      journal,
		RepoPath:        root,
		Timestamp:       brief.CreatedAt,
		Outcome:         brief.VerifiedOutcome,
		ContinuityBrief: &brief,
	}
	if err := appendEvent(journal, milestone); err != nil {
		if removeErr := os.Remove(briefPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("attach Continuity Brief: %w (also could not remove unattached brief: %v)", err, removeErr)
		}
		return err
	}
	if err := notifyCheckpointMilestone(root, journal, brief); err != nil {
		// The milestone and its sidecar are now a complete, recoverable local
		// checkpoint. Leave them intact so a later identical `checkpoint`
		// command can retry only the Entire notification.
		return err
	}
	_, err = stdout.Write(briefData)
	return err
}

// Resume intentionally performs no Aider launch and no filesystem mutation.
// It is the fresh-terminal recovery step: show the validated brief, let the
// developer inspect it, and wait for a separate explicit instruction before
// any new coding session starts.
func Resume(args []string, stdout io.Writer) error {
	fs := flags("resume", args)
	repo := fs.String("repo", "", "repository path")
	resumeID := fs.String("resume", "", "Aider Session name")
	sessionID := fs.String("session", "", "Aider Session name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *resumeID != "" && *sessionID != "" && *resumeID != *sessionID {
		return errors.New("--resume and --session must name the same Aider Session")
	}
	id := *resumeID
	if id == "" {
		id = *sessionID
	}
	if err := validateSessionName(id); err != nil {
		return err
	}
	root, err := filepath.Abs(repoPath(*repo))
	if err != nil {
		return err
	}
	briefData, err := readContinuityBrief(root, id)
	if err != nil {
		return err
	}
	_, err = stdout.Write(briefData)
	return err
}

func readCanonicalSession(root, sessionID string) (string, string, []byte, []journalEvent, error) {
	if err := validateSessionName(sessionID); err != nil {
		return "", "", nil, nil, err
	}
	dir := filepath.Join(sessionDir(root), sessionID)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("Aider Session %q: %w", sessionID, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", nil, nil, errors.New("Aider Session directory is not a private directory")
	}
	journal := filepath.Join(dir, "events.jsonl")
	info, err = os.Lstat(journal)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("Aider Session journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", nil, nil, errors.New("Aider Session journal is not a regular file")
	}
	raw, err := os.ReadFile(journal)
	if err != nil {
		return "", "", nil, nil, err
	}
	safe, canonical, events, err := sanitizeNativeData(raw)
	if err != nil {
		return "", "", nil, nil, err
	}
	if !canonical {
		return "", "", nil, nil, errors.New("Aider Session journal is not canonical Aider data")
	}
	if !bytes.Equal(raw, safe) {
		return "", "", nil, nil, errors.New("Aider Session journal is not redacted canonical data")
	}
	id, err := singleSessionID(events)
	if err != nil {
		return "", "", nil, nil, err
	}
	if id != sessionID {
		return "", "", nil, nil, fmt.Errorf("Aider Session journal %q does not match requested session %q", id, sessionID)
	}
	for _, event := range events {
		if event.SessionRef == "" {
			continue
		}
		if err := validateSessionRef(event.SessionRef, root, sessionID); err != nil {
			return "", "", nil, nil, fmt.Errorf("Aider Session journal has an invalid session_ref: %w", err)
		}
	}
	return dir, journal, raw, events, nil
}

func readContinuityDecision(path string) (continuityDecision, error) {
	if path == "" {
		return continuityDecision{}, errors.New("--brief-file is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return continuityDecision{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return continuityDecision{}, errors.New("continuity decision file is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return continuityDecision{}, err
	}
	var decision continuityDecision
	if err := decodeStrictJSON(data, &decision); err != nil {
		return continuityDecision{}, fmt.Errorf("parse continuity decision: %w", err)
	}
	decision.Goal = redactDecision(decision.Goal)
	decision.Assumptions = sanitizeDecisions(decision.Assumptions)
	decision.Failures = sanitizeDecisions(decision.Failures)
	decision.OpenRisks = sanitizeDecisions(decision.OpenRisks)
	if decision.Goal == "" {
		return continuityDecision{}, errors.New("continuity decision requires a goal")
	}
	return decision, nil
}

func buildContinuityBrief(sessionID string, events []journalEvent, decision continuityDecision) (continuityBrief, error) {
	var (
		model                      string
		lastOutcome                string
		modified, created, deleted []string
		tests                      []testEvidence
	)
	for _, event := range events {
		if event.Model != "" {
			model = event.Model
		}
		modified = append(modified, event.Modified...)
		created = append(created, event.New...)
		deleted = append(deleted, event.Deleted...)
		tests = append(tests, event.TestEvidence...)
		if event.Event == "turn-end" {
			lastOutcome = event.Outcome
		}
	}
	verifiedOutcome := "unverified"
	hasFileEvidence := len(modified) > 0 || len(created) > 0 || len(deleted) > 0
	allTestsPassed := len(tests) > 0
	for _, test := range tests {
		if test.Outcome != "passed" {
			allTestsPassed = false
			break
		}
	}
	if lastOutcome == "failed" {
		verifiedOutcome = "failed"
	} else if lastOutcome == "passed" && hasFileEvidence && allTestsPassed {
		verifiedOutcome = "passed"
	}
	brief := continuityBrief{
		SchemaVersion:   continuityBriefVersion,
		SessionID:       sessionID,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		Goal:            decision.Goal,
		VerifiedOutcome: verifiedOutcome,
		Evidence: continuityEvidence{
			IntegrityDigest: continuityEvidenceDigest(events),
			Model:           model,
			ModifiedFiles:   unique(modified),
			NewFiles:        unique(created),
			DeletedFiles:    unique(deleted),
			TestEvidence:    tests,
		},
		Assumptions: decision.Assumptions,
		Failures:    decision.Failures,
		OpenRisks:   decision.OpenRisks,
	}
	return sanitizeContinuityBrief(brief), nil
}

// continuityEvidenceDigest deliberately excludes workstation-local references.
// Entire can restore a session into another checkout, where SessionRef and
// RepoPath necessarily change even though the decision and its evidence do not.
func continuityEvidenceDigest(events []journalEvent) string {
	lines := make([][]byte, 0, len(events))
	for _, event := range events {
		normalized := sanitizeJournalEvent(event)
		normalized.SessionRef = ""
		normalized.RepoPath = ""
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return contentDigest("unencodable-continuity-evidence")
		}
		lines = append(lines, encoded)
	}
	return contentDigest(string(bytes.Join(lines, []byte("\n"))))
}

func readContinuityBrief(root, sessionID string) ([]byte, error) {
	_, _, journalData, _, err := readCanonicalSession(root, sessionID)
	if err != nil {
		return nil, err
	}
	embedded, prefixDigest, err := latestAttachedBrief(journalData)
	if err != nil {
		return nil, err
	}
	var embeddedBrief continuityBrief
	if err := decodeStrictJSON(embedded, &embeddedBrief); err != nil {
		return nil, fmt.Errorf("parse embedded Continuity Brief: %w", err)
	}
	canonicalBrief := sanitizeContinuityBrief(embeddedBrief)
	canonical, err := json.Marshal(canonicalBrief)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(embedded, canonical) {
		return nil, errors.New("embedded Continuity Brief is not canonical redacted data")
	}
	if canonicalBrief.SessionID != sessionID {
		return nil, errors.New("Continuity Brief does not belong to the requested Aider Session")
	}
	if canonicalBrief.Goal == "" {
		return nil, errors.New("Continuity Brief has no goal")
	}
	if canonicalBrief.Evidence.IntegrityDigest != prefixDigest {
		return nil, errors.New("Continuity Brief integrity digest does not match its journal evidence")
	}

	// Entire restores the canonical transcript, not launcher-local sidecars.
	// The embedded milestone is therefore authoritative and lets a fresh
	// checkout recover without raw histories or a terminal scrollback.
	path := filepath.Join(sessionDir(root), sessionID, continuityBriefFilename)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return canonical, nil
	}
	if err != nil {
		return nil, fmt.Errorf("Continuity Brief: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Continuity Brief is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sidecar continuityBrief
	if err := decodeStrictJSON(data, &sidecar); err != nil {
		return nil, fmt.Errorf("parse Continuity Brief: %w", err)
	}
	safeSidecar := sanitizeContinuityBrief(sidecar)
	sidecarData, err := json.Marshal(safeSidecar)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, sidecarData) {
		return nil, errors.New("Continuity Brief is not canonical redacted data")
	}
	if !bytes.Equal(sidecarData, canonical) {
		return nil, errors.New("Continuity Brief sidecar does not match the canonical journal milestone")
	}
	return canonical, nil
}

func latestAttachedBrief(journalData []byte) ([]byte, string, error) {
	var latest []byte
	var latestDigest string
	var prior []journalEvent
	for _, line := range bytes.SplitAfter(journalData, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var event journalEvent
		if err := json.Unmarshal(trimmed, &event); err != nil {
			return nil, "", fmt.Errorf("parse canonical journal milestone: %w", err)
		}
		if event.Event == "checkpoint-milestone" && event.ContinuityBrief != nil {
			brief, err := json.Marshal(sanitizeContinuityBrief(*event.ContinuityBrief))
			if err != nil {
				return nil, "", err
			}
			latest = brief
			latestDigest = continuityEvidenceDigest(prior)
		}
		prior = append(prior, sanitizeJournalEvent(event))
	}
	if latest == nil {
		return nil, "", errors.New("Continuity Brief is not attached to the canonical journal")
	}
	return latest, latestDigest, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("unexpected second JSON value")
		}
		return err
	}
	return nil
}

func unique(items []string) []string {
	set := map[string]bool{}
	for _, v := range items {
		if v != "" {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// Launch creates an isolated Aider session, journals lifecycle events, and
// then delegates to the real Aider executable. `--aider-bin` makes the
// launcher testable with a fixture; all remaining arguments are forwarded.
func Launch(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("aider-entire", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repo := fs.String("repo", "", "repository path")
	name := fs.String("name", "", "session name")
	message := fs.String("message", "", "disabled for privacy; use --message-file")
	messageFile := fs.String("message-file", "", "one-shot message file")
	intent := fs.String("intent", "", "safe, redacted purpose label for the journal")
	resume := fs.String("resume", "", "retrieve a saved Continuity Brief without launching Aider")
	continueFrom := fs.String("continue-from", "", "start a fresh Aider Session from a saved Continuity Brief")
	aiderBin := fs.String("aider-bin", "aider", "Aider executable")
	model := fs.String("model", "", "Aider model")
	var testCommands stringList
	fs.Var(&testCommands, "test-command", "verification command to run after Aider (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *message != "" {
		return errors.New("raw --message is disabled for privacy; use --message-file")
	}
	if *resume != "" && *continueFrom != "" {
		return errors.New("--resume and --continue-from cannot be used together")
	}
	if err := rejectOwnedAiderArgs(fs.Args()); err != nil {
		return err
	}
	if *resume != "" {
		if *name != "" || *messageFile != "" || strings.TrimSpace(*intent) != "" || *model != "" || *aiderBin != "aider" || len(testCommands) != 0 {
			return errors.New("--resume only retrieves a Continuity Brief; use --continue-from with an explicit --message-file to start a new Aider Session")
		}
		return Resume([]string{"--repo", *repo, "--resume", *resume}, stdout)
	}
	var prompt string
	if *messageFile != "" {
		data, err := os.ReadFile(*messageFile)
		if err != nil {
			return fmt.Errorf("read message-file: %w", err)
		}
		prompt = string(data)
	}
	if prompt == "" && strings.TrimSpace(*intent) == "" {
		return errors.New("--intent is required for an interactive Aider Session")
	}
	root, err := filepath.Abs(repoPath(*repo))
	if err != nil {
		return err
	}
	journalPrompt := prompt
	var (
		previousSessionID string
		previousBrief     *continuityBrief
	)
	if *continueFrom != "" {
		if err := validateSessionName(*continueFrom); err != nil {
			return err
		}
		if journalPrompt == "" {
			return errors.New("--continue-from requires an explicit --message-file instruction")
		}
		briefData, err := readContinuityBrief(root, *continueFrom)
		if err != nil {
			return err
		}
		var brief continuityBrief
		if err := decodeStrictJSON(briefData, &brief); err != nil {
			return err
		}
		previousSessionID = *continueFrom
		previousBrief = &brief
		prompt = continuationPrompt(briefData, journalPrompt)
	}
	if *name == "" {
		*name = "aider-" + time.Now().UTC().Format("20060102-150405.000000000")
	}
	if err := validateSessionName(*name); err != nil {
		return err
	}
	if previousSessionID != "" && *name == previousSessionID {
		return errors.New("--continue-from must create a fresh Aider Session with a new --name")
	}
	release, err := acquireEvidenceLock(root, *name)
	if err != nil {
		return err
	}
	defer release()
	dir, err := createSessionDirectory(root, *name, false)
	if err != nil {
		return err
	}
	journal := filepath.Join(dir, "events.jsonl")
	chat := filepath.Join(dir, "chat.history.md")
	inputHistory := filepath.Join(dir, "input.history")
	llm := filepath.Join(dir, "llm.history")
	stagedPrompt, removePrompt, err := stagePrompt(dir, prompt)
	if err != nil {
		return err
	}
	defer removePrompt()
	before := gitStatus(root)
	startTime := time.Now()
	start := journalEvent{Event: "session-start", SessionID: *name, PreviousSessionID: previousSessionID, SessionRef: journal, RepoPath: root, Timestamp: startTime.UTC().Format(time.RFC3339), Model: *model, Outcome: "running", ContinuityBrief: previousBrief}
	if err := appendAndNotify(root, journal, start); err != nil {
		return err
	}
	if prompt != "" || strings.TrimSpace(*intent) != "" {
		if err := appendAndNotify(root, journal, journalEvent{Event: "turn-start", SessionID: *name, SessionRef: journal, RepoPath: root, Timestamp: time.Now().UTC().Format(time.RFC3339), PromptDigest: promptDigest(journalPrompt, *intent), Model: *model, Outcome: "running"}); err != nil {
			return err
		}
	}
	forward := []string{"--chat-history-file", chat, "--input-history-file", inputHistory, "--llm-history-file", llm, "--no-auto-commits"}
	if *model != "" {
		forward = append(forward, "--model", *model)
	}
	if stagedPrompt != "" {
		// Do not place developer text in the process argument list. The file is
		// private to this run and removed immediately after Aider exits.
		forward = append(forward, "--message-file", stagedPrompt, "--no-stream")
	}
	forward = append(forward, fs.Args()...)
	cmd := exec.Command(*aiderBin, forward...)
	cmd.Dir = root
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	agentErr := cmd.Run()
	var (
		tests    []testEvidence
		testErr  error
		testCode int
	)
	if agentErr == nil && len(testCommands) > 0 {
		tests, testCode, testErr = runVerificationCommands(root, testCommands, stdout, stderr)
	}
	after := gitStatus(root)
	modified, created, deleted := diffStatus(before, after)
	runErr := agentErr
	if runErr == nil {
		runErr = testErr
	}
	code := 0
	failureKind := ""
	if agentErr != nil {
		code = commandExitCode(agentErr)
		if _, ok := agentErr.(*exec.ExitError); ok {
			failureKind = "aider-exit-nonzero"
		} else {
			failureKind = "aider-launch-error"
		}
	} else if testErr != nil {
		code = testCode
		failureKind = "verification-failed"
	}
	end := journalEvent{Event: "turn-end", SessionID: *name, SessionRef: journal, RepoPath: root, Timestamp: time.Now().UTC().Format(time.RFC3339), Model: *model, Modified: modified, New: created, Deleted: deleted, TestEvidence: tests, Outcome: "passed", DurationMS: time.Since(startTime).Milliseconds()}
	if runErr != nil {
		end.Outcome = "failed"
		end.FailureKind = failureKind
	}
	end.ExitCode = &code
	if appendErr := appendAndNotify(root, journal, end); appendErr != nil {
		return appendErr
	}
	if appendErr := appendAndNotify(root, journal, journalEvent{Event: "session-end", SessionID: *name, SessionRef: journal, RepoPath: root, Timestamp: time.Now().UTC().Format(time.RFC3339), Outcome: end.Outcome, DurationMS: end.DurationMS, ExitCode: end.ExitCode, FailureKind: end.FailureKind}); appendErr != nil {
		return appendErr
	}
	return runErr
}

func createSessionDirectory(root, name string, resume bool) (string, error) {
	base := sessionDir(root)
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(base, 0700); err != nil {
		return "", err
	}
	dir := filepath.Join(base, name)
	if resume {
		info, err := os.Lstat(dir)
		if err != nil {
			return "", fmt.Errorf("resume session %q: %w", name, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("resume session directory is not a private directory")
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return "", err
		}
	} else if err := os.Mkdir(dir, 0700); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("Aider Session %q already exists; use --resume %q to continue it", name, name)
		}
		return "", err
	}
	if err := ensureHistoryFiles(dir, resume); err != nil {
		return "", err
	}
	if resume {
		if err := validateResumableJournal(root, name, dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func validateResumableJournal(root, sessionID, dir string) error {
	path := filepath.Join(dir, "events.jsonl")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("resume session %q: %w", sessionID, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("resume journal is not a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	safe, journal, events, err := sanitizeNativeData(raw)
	if err != nil {
		return err
	}
	if !journal {
		return errors.New("resume journal is not canonical Aider session data")
	}
	id, err := singleSessionID(events)
	if err != nil {
		return err
	}
	if id != sessionID {
		return fmt.Errorf("resume journal session %q does not match requested session %q", id, sessionID)
	}
	for _, event := range events {
		if event.SessionRef == "" {
			continue
		}
		if err := validateSessionRef(event.SessionRef, root, sessionID); err != nil {
			return fmt.Errorf("resume journal has an invalid session_ref: %w", err)
		}
	}
	if err := validateSessionRef(path, root, sessionID); err != nil {
		return err
	}
	if !bytes.Equal(raw, safe) {
		return writePrivateFile(path, safe)
	}
	return nil
}

func ensureHistoryFiles(dir string, resume bool) error {
	for _, name := range []string{"chat.history.md", "input.history", "llm.history"} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("Aider history %q is not a regular file", name)
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if resume {
			return fmt.Errorf("resume session is missing %s", name)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if chmodErr := f.Chmod(0600); chmodErr != nil {
			_ = f.Close()
			return chmodErr
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// A per-repository lock serializes working-tree snapshots. Aider sessions may
// coexist over time, but simultaneous runs cannot claim one another's edits.
func acquireEvidenceLock(root, sessionID string) (func(), error) {
	base := sessionDir(root)
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(base, ".active-evidence.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, errors.New("another Aider Session is already recording evidence in this repository")
		}
		return nil, err
	}
	_, writeErr := io.WriteString(f, sessionID+"\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if writeErr != nil {
			return nil, writeErr
		}
		return nil, closeErr
	}
	return func() { _ = os.Remove(path) }, nil
}

func stagePrompt(dir, prompt string) (string, func(), error) {
	if prompt == "" {
		return "", func() {}, nil
	}
	f, err := os.CreateTemp(dir, ".aider-message-*")
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := io.WriteString(f, prompt); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// continuationPrompt keeps the recovered context and the new developer
// instruction in the same short-lived private message file. It is assembled
// only after the developer explicitly opts into --continue-from.
func continuationPrompt(briefData []byte, instruction string) string {
	return "Redacted Continuity Brief from the previous Aider Session:\n" + string(briefData) +
		"\n\nExplicit developer instruction for this new session:\n" + instruction
}

func runVerificationCommands(repo string, commands []string, stdout, stderr io.Writer) ([]testEvidence, int, error) {
	results := make([]testEvidence, 0, len(commands))
	firstFailure := 0
	for _, command := range commands {
		started := time.Now()
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/C", command)
		} else {
			cmd = exec.Command("sh", "-c", command)
		}
		cmd.Dir = repo
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		err := cmd.Run()
		result := testEvidence{CommandDigest: contentDigest(command), Outcome: "passed", ExitCode: 0, DurationMS: time.Since(started).Milliseconds()}
		if err != nil {
			result.Outcome = "failed"
			result.ExitCode = commandExitCode(err)
			if firstFailure == 0 {
				firstFailure = result.ExitCode
			}
		}
		results = append(results, result)
	}
	if firstFailure != 0 {
		return results, firstFailure, errors.New("one or more verification commands failed")
	}
	return results, 0, nil
}

func commandExitCode(err error) int {
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return -1
}

// These flags define the continuity boundary. Passthrough arguments are
// allowed for normal Aider behavior, but they must not replace the histories,
// message transport, model attribution, or resume behavior owned by this
// launcher.
var ownedAiderFlags = map[string]bool{
	"--chat-history-file":       true,
	"--input-history-file":      true,
	"--llm-history-file":        true,
	"--message":                 true,
	"--msg":                     true,
	"-m":                        true,
	"--message-file":            true,
	"-f":                        true,
	"--model":                   true,
	"--restore-chat-history":    true,
	"--no-restore-chat-history": true,
	"--auto-commits":            true,
	"--no-auto-commits":         true,
}

func rejectOwnedAiderArgs(args []string) error {
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if isOwnedAiderFlag(name) {
			return fmt.Errorf("%s is owned by aider-entire and cannot be passed through", name)
		}
	}
	return nil
}

func isOwnedAiderFlag(name string) bool {
	if ownedAiderFlags[name] || (strings.HasPrefix(name, "-m") && name != "-") || (strings.HasPrefix(name, "-f") && name != "-") {
		return true
	}
	if !strings.HasPrefix(name, "--") {
		return false
	}
	for owned := range ownedAiderFlags {
		if strings.HasPrefix(owned, "--") && strings.HasPrefix(owned, name) {
			// argparse accepts unambiguous long-option abbreviations.
			return true
		}
	}
	return false
}

func appendEvent(path string, event journalEvent) error {
	event = sanitizeJournalEvent(event)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to append Aider journal data through a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
func appendAndNotify(repo, journal string, event journalEvent) error {
	event = sanitizeJournalEvent(event)
	if err := appendEvent(journal, event); err != nil {
		return err
	}
	return notifyEntire(repo, event)
}

// notifyCheckpointMilestone asks Entire to capture the already-appended
// checkpoint-milestone through a supported lifecycle hook. It deliberately
// does not append a synthetic turn-end event: the canonical transcript keeps
// the real milestone as the authoritative handoff record.
func notifyCheckpointMilestone(repo, journal string, brief continuityBrief) error {
	return notifyEntire(repo, journalEvent{
		Event:           "turn-end",
		SessionID:       brief.SessionID,
		SessionRef:      journal,
		RepoPath:        repo,
		Timestamp:       brief.CreatedAt,
		Outcome:         brief.VerifiedOutcome,
		ContinuityBrief: &brief,
	})
}

func notifyEntire(repo string, event journalEvent) error {
	event = sanitizeJournalEvent(event)
	// Before `entire enable`, Aider remains a normal standalone CLI. After
	// enable, a missing Entire binary is actionable rather than silently losing
	// a checkpoint lifecycle event.
	if _, err := os.Stat(markerPath(repo)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	bin, err := exec.LookPath("entire")
	if err != nil {
		return errors.New("Entire is enabled for Aider but the `entire` command is not on PATH")
	}
	payload, err := notificationPayload(event)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "hooks", "aider", event.Event)
	cmd.Dir = repo
	cmd.Stdin = bytes.NewReader(payload)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not notify Entire of the recorded Aider event (%s): %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func notificationPayload(event journalEvent) ([]byte, error) {
	return json.Marshal(sanitizeJournalEvent(event))
}
func gitStatus(repo string) map[string]string {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return map[string]string{}
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		result[path] = line[:2] + "|" + workingTreeFingerprint(repo, path)
	}
	return result
}

func workingTreeFingerprint(repo, path string) string {
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(path)))
	if err == nil {
		return contentDigest(string(data))
	}
	if os.IsNotExist(err) {
		return "missing"
	}
	return "unreadable"
}

func diffStatus(before, after map[string]string) (modified, created, deleted []string) {
	for path, state := range after {
		if strings.HasPrefix(filepath.ToSlash(path), ".entire/") {
			continue
		}
		previous, existed := before[path]
		if existed && previous == state {
			continue
		}
		status := strings.SplitN(state, "|", 2)[0]
		if strings.Contains(status, "D") {
			deleted = append(deleted, path)
		} else if !existed && strings.Contains(status, "?") {
			created = append(created, path)
		} else {
			modified = append(modified, path)
		}
	}
	for path := range before {
		if strings.HasPrefix(filepath.ToSlash(path), ".entire/") {
			continue
		}
		if _, ok := after[path]; !ok {
			deleted = append(deleted, path)
		}
	}
	return unique(modified), unique(created), unique(deleted)
}
