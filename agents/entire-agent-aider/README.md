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

A fresh `--name` is atomic and refuses to reuse an existing session; a
per-repository evidence lock also prevents concurrent runs from attributing the
same working-tree changes to two sessions. `--test-command` is repeatable and
records only a SHA-256 command fingerprint, outcome, exit code, and duration in
the journal—never command text or output.

One-shot prompts must use a private `--message-file`; the launcher copies it to
a short-lived private file for Aider and deletes that copy when the run ends.
This keeps prompt text out of command arguments. Keep the Groq API key in the
environment rather than a launcher argument. Interactive sessions must supply
`--intent "safe purpose label"` so there is meaningful, redacted continuity
context from the start.

## Explicit checkpoint and safe recovery

At a meaningful milestone, create a small JSON decision file with curated,
shareable labels—not a raw prompt or model response:

```json
{
  "goal": "Validate checkout idempotency",
  "assumptions": ["The provider retains idempotency keys for 24 hours"],
  "failures": ["The first staging replay timed out"],
  "open_risks": ["Legacy clients may retry after the retention window"]
}
```

Attach it to the isolated session with an explicit milestone:

```sh
aider-entire checkpoint --session checkout-fix --brief-file ./checkpoint-decision.json
```

This writes a private `continuity-brief.json` beside the session and embeds the
same redacted brief in the canonical journal. When this repository has been
enabled with `entire enable --agent aider`, the command then notifies Entire
through its supported `turn-end` hook so Entire captures that journal
milestone. Without that enablement, the local journal and sidecar remain a
durable checkpoint but no Entire notification is attempted. The brief contains
the goal, an honest verification status, changed-file and test evidence,
assumptions, failures, and open risks. It never includes raw prompts, model
responses, credentials, test command text, or test output.

Each Aider Session has one durable Continuity Brief. If the Entire notification
fails after the brief is written, the command exits nonzero but leaves the
validated sidecar and journal milestone intact. After Entire is available,
retry only that notification—without launching Aider, changing code, or
creating a second milestone—with:

```sh
aider-entire checkpoint --session checkout-fix
```

On this retry path `--brief-file` is not needed (and a new decision file does
not replace the existing brief).

From a fresh terminal, after Entire has restored the canonical
`.entire/aider-sessions/<id>/events.jsonl` transcript into the checkout,
retrieve the brief without starting Aider or modifying code. A launcher-local
sidecar is optional after restoration: the command validates and reconstructs
the brief from its embedded journal milestone.

```sh
aider-entire --repo /path/to/restored-checkout --resume checkout-fix
# equivalent: aider-entire resume --session checkout-fix
```

The command prints only the stored JSON brief and exits. When the developer is
ready to make a new change, they must explicitly provide a fresh instruction:

```sh
aider-entire --continue-from checkout-fix --name checkout-followup \
  --intent "Apply the reviewed follow-up" \
  --message-file ./next-instruction.md
```

`--continue-from` creates a new isolated Aider Session with fresh private
histories and a safe link to the prior brief; it never restores or exposes the
old raw Aider history.
