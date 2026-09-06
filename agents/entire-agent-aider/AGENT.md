# Aider — External Agent Research

## Verdict: COMPATIBLE

The integration owns a stable Aider session boundary with `aider-entire`.
Aider's supported CLI accepts explicit history paths and one-shot messages;
it does not offer a stable lifecycle hook API, so the launcher writes the
authoritative JSONL lifecycle journal.

## Static Checks

| Check | Result | Notes |
|---|---|---|
| Binary present | UNVERIFIED | `aider` was not required for this fixture-based build |
| Documentation | PASS | Aider scripting documentation |
| Hook mechanism | PASS | launcher-owned JSONL events |

## Protocol Mapping

| Subcommand | Implementation |
|---|---|
| `info`, `detect` | static metadata; `aider` PATH lookup |
| session helpers | `.entire/aider-sessions/<id>/events.jsonl` |
| read/write/chunk transcript | append-only journal bytes |
| hooks | launcher event payload parsed by `parse-hook`; install writes a repo marker |
| transcript analyzer | scans typed journal events and exposes the latest milestone brief as a safe summary |
| resume command | `aider-entire --resume '<id>'` validates and prints a Continuity Brief without launching Aider |

## Selected Capabilities

| Capability | Declared | Justification |
|---|---:|---|
| hooks | true | launcher owns lifecycle events |
| transcript_analyzer | true | JSONL journal has redacted prompt digests and file deltas |
| transcript_preparer | false | journal is already canonical |
| token_calculator | false | Aider history is not reliable token accounting |

## Storage

- Session directory: `.entire/aider-sessions/`
- Canonical transcript: `<id>/events.jsonl` (redacted-only continuity data)
- Continuity Brief: `<id>/continuity-brief.json`, embedded again in an explicit
  `checkpoint-milestone` journal event so Entire can retain it with the session
  checkpoint. After `entire enable --agent aider`, checkpoint also invokes
  Entire's supported `turn-end` hook to capture that already-appended milestone.
  A failed notification leaves the valid local milestone available for an
  explicit notification-only retry.
- Supporting raw history: `chat.history.md`, `input.history`, `llm.history`
  (private `0600` Aider-local state; never exposed through the protocol)
- Verification: unit fixture exercises session discovery, prompt extraction,
  malformed hook errors, redaction across all protocol boundaries, evidence
  isolation, explicit checkpoint/recovery, and a fake Aider executable.

## E2E Prerequisites

- Entire CLI on `PATH`
- Aider CLI on `PATH` for live workflow tests
- `aider-entire` and `entire-agent-aider` installed on `PATH`

## Verification Script

`scripts/verify-aider.sh` creates a temporary git repository and fake Aider
executable, then verifies the exact launcher journal, redacted hook payload,
explicit local checkpoint, and no-Aider resume shape. Unit tests additionally
cover the enabled-Entire notification/retry path and sidecar-less transcript
restore. The script is non-destructive and needs neither credentials nor a live
model.
