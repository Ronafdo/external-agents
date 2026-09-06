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
| transcript analyzer | scans typed journal events |
| resume command | `aider-entire resume '<id>'` |

## Selected Capabilities

| Capability | Declared | Justification |
|---|---:|---|
| hooks | true | launcher owns lifecycle events |
| transcript_analyzer | true | JSONL journal has prompts and file deltas |
| transcript_preparer | false | journal is already canonical |
| token_calculator | false | Aider history is not reliable token accounting |

## Storage

- Session directory: `.entire/aider-sessions/`
- Canonical transcript: `<id>/events.jsonl`
- Supporting raw history: `chat.history.md`, `input.history`, `llm.history`
- Verification: unit fixture exercises session discovery, prompt extraction,
  malformed hook errors, and a fake Aider executable.

## E2E Prerequisites

- Entire CLI on `PATH`
- Aider CLI on `PATH` for live workflow tests
- `aider-entire` and `entire-agent-aider` installed on `PATH`

## Verification Script

`scripts/verify-aider.sh` creates a temporary git repository and fake Aider
executable, then verifies the exact launcher journal and hook payload shape.
It is non-destructive and needs neither credentials nor a live model.
