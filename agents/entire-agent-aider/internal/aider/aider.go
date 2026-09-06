// Package aider implements the Entire protocol around sessions created by the
// aider-entire launcher. The launcher owns the durable boundary because Aider
// does not expose stable lifecycle hooks.
package aider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const protocolVersion = 1

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
	Event      string   `json:"event"`
	SessionID  string   `json:"session_id"`
	SessionRef string   `json:"session_ref,omitempty"`
	RepoPath   string   `json:"repo_path,omitempty"`
	Timestamp  string   `json:"timestamp"`
	Prompt     string   `json:"prompt,omitempty"`
	Model      string   `json:"model,omitempty"`
	Modified   []string `json:"modified_files,omitempty"`
	New        []string `json:"new_files,omitempty"`
	Deleted    []string `json:"deleted_files,omitempty"`
	ExitCode   int      `json:"exit_code,omitempty"`
	Error      string   `json:"error,omitempty"`
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
		if *id == "" {
			return errors.New("session-id is required")
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
		return copyFile(stdout, *ref)
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
		if *id == "" {
			return errors.New("session-id is required")
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
		return writeJSON(stdout, map[string]any{"summary": "", "has_summary": false})
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
func copyFile(w io.Writer, path string) error {
	if path == "" {
		return errors.New("session-ref is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
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
	events, err := readEvents(ref)
	if err != nil {
		return err
	}
	id := input.SessionID
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
	native, err := os.ReadFile(ref)
	if err != nil {
		return err
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
	if err := os.MkdirAll(filepath.Dir(s.SessionRef), 0750); err != nil {
		return err
	}
	return os.WriteFile(s.SessionRef, s.NativeData, 0600)
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
	typ := map[string]int{"session-start": 1, "turn-start": 2, "turn-end": 3, "session-end": 5}[*hook]
	if typ == 0 {
		typ = map[string]int{"session-start": 1, "turn-start": 2, "turn-end": 3, "session-end": 5}[e.Event]
	}
	if typ == 0 {
		return writeJSON(stdout, nil)
	}
	return writeJSON(stdout, map[string]any{"type": typ, "session_id": e.SessionID, "session_ref": e.SessionRef, "prompt": e.Prompt, "model": e.Model, "timestamp": e.Timestamp})
}
func installHooks(args []string, stdout io.Writer) error {
	fs := flags("install-hooks", args)
	_ = fs.Bool("local-dev", false, "local")
	_ = fs.Bool("force", false, "force")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := markerPath(repoPath(""))
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
	if *off < 0 || *off > int64(len(data)) {
		return errors.New("offset is outside transcript")
	}
	events, err := eventsFrom(data[*off:])
	if err != nil {
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
	if *off < 0 || *off > int64(len(data)) {
		return errors.New("offset is outside transcript")
	}
	events, err := eventsFrom(data[*off:])
	if err != nil {
		return err
	}
	var prompts []string
	for _, e := range events {
		if e.Event == "turn-start" && e.Prompt != "" {
			prompts = append(prompts, e.Prompt)
		}
	}
	return writeJSON(stdout, map[string]any{"prompts": prompts})
}
func readEvents(path string) ([]journalEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return eventsFrom(data)
}
func eventsFrom(data []byte) ([]journalEvent, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 4*1024*1024)
	var out []journalEvent
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var e journalEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("parse journal: %w", err)
		}
		out = append(out, e)
	}
	return out, scanner.Err()
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
	message := fs.String("message", "", "one-shot message")
	messageFile := fs.String("message-file", "", "one-shot message file")
	resume := fs.String("resume", "", "resume an existing session")
	aiderBin := fs.String("aider-bin", "aider", "Aider executable")
	model := fs.String("model", "", "Aider model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *message != "" && *messageFile != "" {
		return errors.New("use either --message or --message-file, not both")
	}
	root, err := filepath.Abs(repoPath(*repo))
	if err != nil {
		return err
	}
	if *resume != "" {
		*name = *resume
	}
	if *name == "" {
		*name = "aider-" + time.Now().UTC().Format("20060102-150405.000000000")
	}
	if strings.ContainsAny(*name, `/\\`) || *name == "." || *name == ".." {
		return errors.New("session name must not contain path separators")
	}
	dir := filepath.Join(sessionDir(root), *name)
	if *resume != "" {
		if _, err := os.Stat(filepath.Join(dir, "chat.history.md")); err != nil {
			return fmt.Errorf("resume session %q: %w", *resume, err)
		}
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	journal := filepath.Join(dir, "events.jsonl")
	chat := filepath.Join(dir, "chat.history.md")
	inputHistory := filepath.Join(dir, "input.history")
	llm := filepath.Join(dir, "llm.history")
	before := gitStatus(root)
	start := journalEvent{Event: "session-start", SessionID: *name, SessionRef: journal, RepoPath: root, Timestamp: time.Now().UTC().Format(time.RFC3339), Model: *model}
	if err := appendAndNotify(root, journal, start); err != nil {
		return err
	}
	var prompt string
	if *message != "" {
		prompt = *message
	}
	if *messageFile != "" {
		data, err := os.ReadFile(*messageFile)
		if err != nil {
			return fmt.Errorf("read message-file: %w", err)
		}
		prompt = string(data)
	}
	if prompt != "" {
		if err := appendAndNotify(root, journal, journalEvent{Event: "turn-start", SessionID: *name, SessionRef: journal, RepoPath: root, Timestamp: time.Now().UTC().Format(time.RFC3339), Prompt: prompt, Model: *model}); err != nil {
			return err
		}
	}
	forward := []string{"--chat-history-file", chat, "--input-history-file", inputHistory, "--llm-history-file", llm, "--no-auto-commits"}
	if *resume != "" {
		forward = append(forward, "--restore-chat-history")
	}
	if *model != "" {
		forward = append(forward, "--model", *model)
	}
	if *messageFile != "" {
		forward = append(forward, "--message-file", *messageFile, "--no-stream")
	} else if *message != "" {
		forward = append(forward, "--message", *message, "--no-stream")
	}
	forward = append(forward, fs.Args()...)
	cmd := exec.Command(*aiderBin, forward...)
	cmd.Dir = root
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	after := gitStatus(root)
	modified, created, deleted := diffStatus(before, after)
	end := journalEvent{Event: "turn-end", SessionID: *name, SessionRef: journal, RepoPath: root, Timestamp: time.Now().UTC().Format(time.RFC3339), Model: *model, Modified: modified, New: created, Deleted: deleted}
	if err != nil {
		end.Error = err.Error()
		if exit, ok := err.(*exec.ExitError); ok {
			end.ExitCode = exit.ExitCode()
		} else {
			end.ExitCode = -1
		}
	}
	if appendErr := appendAndNotify(root, journal, end); appendErr != nil {
		return appendErr
	}
	if appendErr := appendAndNotify(root, journal, journalEvent{Event: "session-end", SessionID: *name, SessionRef: journal, RepoPath: root, Timestamp: time.Now().UTC().Format(time.RFC3339)}); appendErr != nil {
		return appendErr
	}
	return err
}

func appendEvent(path string, event journalEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
func appendAndNotify(repo, journal string, event journalEvent) error {
	if err := appendEvent(journal, event); err != nil {
		return err
	}
	// Before `entire enable`, Aider remains a normal standalone CLI. After
	// enable, a missing Entire binary is actionable rather than silently losing
	// a checkpoint lifecycle event.
	if _, err := os.Stat(markerPath(repo)); err != nil {
		return nil
	}
	bin, err := exec.LookPath("entire")
	if err != nil {
		return errors.New("Entire is enabled for Aider but the `entire` command is not on PATH")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "hooks", "aider", event.Event)
	cmd.Dir = repo
	cmd.Stdin = bytes.NewReader(payload)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("recorded Aider event but could not notify Entire (%s): %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
func gitStatus(repo string) map[string]string {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return map[string]string{}
	}
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) < 4 {
			continue
		}
		result[strings.TrimSpace(line[3:])] = line[:2]
	}
	return result
}
func diffStatus(before, after map[string]string) (modified, created, deleted []string) {
	for path, status := range after {
		if strings.HasPrefix(filepath.ToSlash(path), ".entire/") {
			continue
		}
		if _, ok := before[path]; !ok && strings.Contains(status, "?") {
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
