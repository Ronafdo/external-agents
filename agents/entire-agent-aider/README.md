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
redacted brief in the canonical source journal. When this repository has been
enabled with `entire enable --agent aider`, the command creates a separate,
redacted, content-addressed `aider-milestone-…` carrier session containing an
advisory brief and its binding manifest. The manifest contains redaction-
invariant structural `*_id` values for the source session, the source Brief
payload hash excluding its milestone ID, and the source evidence hash; together
they derive the content-addressed carrier ID. Entire may redact human-readable
Brief prose during attach or restore, so restored prose is redacted advisory
context rather than byte-for-byte evidence. The source-local Brief and its
evidence remain strictly validated against the canonical source journal. It
then uses Entire's real persistence path:

```sh
entire session attach aider-milestone-… --agent aider
```

This is deliberately not a synthetic `turn-end` hook: an explicit milestone
may contain no new file delta, and a hook would not prove a checkpoint was
written. The launcher never passes `--force` and disables interactive Git
prompts, so it never silently amends a developer commit. If Entire chooses its
manual path and emits an `Entire-Checkpoint:` trailer, the launcher forwards
that trailer to stderr; apply it yourself before treating the milestone as
committed. Without enablement, the local journal and sidecar remain a local
recovery record and no Entire capture is claimed. The brief contains the goal,
an honest verification status, changed-file and test evidence, assumptions,
failures, and open risks. It never includes raw prompts, model responses,
credentials, test command text, or test output.

Each Aider Session has one durable Continuity Brief. Its private publication
receipt progresses from `prepared` to `attaching` to `attached`. Only a
confirmed pre-launch/no-write failure returns to `prepared`, leaving the
validated sidecar, source milestone, and redacted carrier intact for an
explicit retry that neither launches Aider nor changes code:

```sh
aider-entire checkpoint --session checkout-fix
```

On this retry path `--brief-file` is not needed (and a new decision file does
not replace the existing brief). Any other attach that starts without a confirmed
success is ambiguous. If a crash leaves a carrier receipt missing, malformed,
or `attaching`, the launcher reconciles only with positive evidence: it checks
`entire session info <carrier> --json` and, when needed,
`entire checkpoint list --session <carrier> --json`. An empty list, a failed
query, or any other result that does not prove attachment is not proof of
absence. The launcher reports an unknown publication state and refuses a
duplicate attach.

From a fresh terminal, retrieve the brief without starting Aider or modifying
code. Every brief declares a content-addressed `milestone_session_id`. After
Entire restores the redacted carrier, use that exact ID with `--resume` or
`--continue-from`; the launcher deliberately does not search carrier
directories by original source session ID. A launcher-local source sidecar is
optional: when it exists, the command strictly validates it against the source
journal milestone. For a restored carrier, it validates the structural binding
manifest and presents any Entire-redacted Brief prose as advisory context.

```sh
aider-entire --repo /path/to/restored-checkout \
  --resume <milestone_session_id>
# equivalent: aider-entire resume --session <milestone_session_id>
```

The command prints only the stored JSON brief and exits. When the developer is
ready to make a new change, they must explicitly provide a fresh instruction:

```sh
aider-entire --continue-from <milestone_session_id> \
  --name checkout-followup \
  --intent "Apply the reviewed follow-up" \
  --message-file ./next-instruction.md
```

`--continue-from` creates a new isolated Aider Session with fresh private
histories and a safe link to the prior brief; it never restores or exposes the
old raw Aider history.
