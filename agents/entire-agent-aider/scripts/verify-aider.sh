#!/usr/bin/env sh
# Verify the launcher-owned Aider lifecycle without modifying Aider config.
set -eu

agent_bin="${AGENT_BIN:-./entire-agent-aider}"
launcher_bin="${LAUNCHER_BIN:-./aider-entire}"
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

"$launcher_bin" --repo "$repo_arg" --name verifier --aider-bin "$fake_aider" --message 'fixture prompt'
journal="$repo_dir/.entire/aider-sessions/verifier/events.jsonl"
test -s "$journal"
printf 'captured journal: %s\n' "$journal"
cat "$journal"

printf '%s\n' '{"event":"turn-start","session_id":"verifier","timestamp":"2026-09-06T00:00:00Z","prompt":"fixture prompt"}' \
  | "$agent_bin" parse-hook --hook turn-start
printf '%s\n' '{bad json' | "$agent_bin" parse-hook --hook turn-start >/dev/null 2>/dev/null && exit 1 || true
echo 'PASS: fixture session was created, inspectable, and malformed hook input failed safely.'
