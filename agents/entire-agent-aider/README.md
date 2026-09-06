# entire-agent-aider

`entire-agent-aider` lets Entire identify and inspect sessions created by the
included `aider-entire` launcher. The launcher creates an isolated session at
`.entire/aider-sessions/<name>/`, records a redacted-only `events.jsonl`, and
passes explicit, private Aider history paths to the real CLI. Raw prompts are
sent to Aider through a short-lived `0600` message file rather than Aider's
process argv; they never enter Entire's journal, hook payload, or transcript
APIs.

```sh
mise run build
cp entire-agent-aider aider-entire /usr/local/bin/
entire enable --agent aider
aider-entire --name checkout-fix \
  --intent "Validate checkout input" \
  --message-file ./checkout-request.md \
  --model groq/llama-3.3-70b-versatile \
  --test-command "go test ./..."
```

For repeatable development or tests, `aider-entire --aider-bin /path/to/fake`
uses a fixture executable. Aider itself must be available on `PATH` for
`entire-agent-aider detect` to report present.

Use `--resume <name>` to reopen an existing session. A fresh `--name` is
atomic and refuses to reuse an existing session; a per-repository evidence lock
also prevents concurrent runs from attributing the same working-tree changes to
two sessions. `--test-command` is repeatable and records only a SHA-256 command
fingerprint, outcome, exit code, and duration in the journal—never command
text or output.

One-shot prompts must use a private `--message-file`; the launcher copies it to
a short-lived private file for Aider and deletes that copy when the run ends.
This keeps prompt text out of command arguments. Keep the Groq API key in the
environment rather than a launcher argument. Interactive sessions must supply
`--intent "safe purpose label"` so there is meaningful, redacted continuity
context from the start.
