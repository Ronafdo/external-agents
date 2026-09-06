#!/usr/bin/env sh
# Verify the launcher-owned Aider lifecycle without modifying Aider config.
set -eu

agent_bin="${AGENT_BIN:-./entire-agent-aider}"
launcher_bin="${LAUNCHER_BIN:-./aider-entire}"
if [ ! -f "$agent_bin" ] && [ -f "${agent_bin}.exe" ]; then agent_bin="${agent_bin}.exe"; fi
if [ ! -f "$launcher_bin" ] && [ -f "${launcher_bin}.exe" ]; then launcher_bin="${launcher_bin}.exe"; fi
probe_dir="$(pwd)/.probe-aider-$$"
repo_dir="$probe_dir/repo"
keep=0

cleanup() {
  [ "$keep" = 1 ] || rm -rf "$probe_dir"
}
trap cleanup EXIT INT TERM

if [ "${1:-}" = "--keep" ]; then keep=1; fi

mkdir -p "$repo_dir"
git -C "$repo_dir" init -q
cat > "$probe_dir/fake-aider" <<'EOF'
#!/usr/bin/env sh
printf 'fake aider completed\n'
EOF
chmod +x "$probe_dir/fake-aider"
cat > "$probe_dir/fake-aider.cmd" <<'EOF'
@echo off
echo fake aider completed
EOF

fake_aider="$probe_dir/fake-aider"
repo_arg="$repo_dir"
if file "$launcher_bin" | grep -qi 'PE32'; then
  fake_aider="$probe_dir/fake-aider.cmd"
  repo_arg="$(wslpath -w "$repo_dir")"
  fake_aider="$(wslpath -w "$fake_aider")"
fi

printf 'aider binary: '
command -v aider || printf 'WARN (not on PATH; using fixture)\n'
"$agent_bin" info
"$agent_bin" detect

prompt_file="$probe_dir/fixture-prompt.md"
printf '%s\n' 'fixture prompt' > "$prompt_file"
prompt_arg="$prompt_file"
if file "$launcher_bin" | grep -qi 'PE32'; then
  prompt_arg="$(wslpath -w "$prompt_file")"
fi
"$launcher_bin" --repo "$repo_arg" --name verifier --aider-bin "$fake_aider" \
  --intent 'verify fixture session' --message-file "$prompt_arg" \
  --test-command 'echo fixture verification'
journal="$repo_dir/.entire/aider-sessions/verifier/events.jsonl"
test -s "$journal"
grep -q '"prompt_digest"' "$journal"
grep -q '"test_evidence"' "$journal"
! grep -q 'fixture prompt' "$journal"

decision_file="$probe_dir/continuity-decision.json"
printf '%s\n' '{"goal":"Verify safe session recovery","assumptions":["The canonical journal is available"],"failures":[],"open_risks":["A developer must explicitly choose the next instruction"]}' > "$decision_file"
decision_arg="$decision_file"
if file "$launcher_bin" | grep -qi 'PE32'; then
  decision_arg="$(wslpath -w "$decision_file")"
fi
"$launcher_bin" checkpoint --repo "$repo_arg" --session verifier --brief-file "$decision_arg"
brief="$repo_dir/.entire/aider-sessions/verifier/continuity-brief.json"
test -s "$brief"
! grep -q 'fixture prompt' "$brief"
resumed="$probe_dir/resumed-brief.json"
"$launcher_bin" --repo "$repo_arg" --resume verifier > "$resumed"
cmp -s "$brief" "$resumed"

printf 'captured journal: %s\n' "$journal"
cat "$journal"

hook_payload="$(printf '%s\n' '{"event":"turn-start","session_id":"verifier","timestamp":"2026-09-06T00:00:00Z","prompt":"fixture prompt"}' \
  | "$agent_bin" parse-hook --hook turn-start)"
printf '%s\n' "$hook_payload"
printf '%s' "$hook_payload" | grep -q 'prompt_sha256:'
! printf '%s' "$hook_payload" | grep -q 'fixture prompt'
printf '%s\n' '{bad json' | "$agent_bin" parse-hook --hook turn-start >/dev/null 2>/dev/null && exit 1 || true
echo 'PASS: fixture session was isolated, redacted, checkpointed, safely recoverable, and malformed hook input failed safely.'
