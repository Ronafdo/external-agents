# Aider Continuum

## Problem Statement

Developers using Aider lose the reasoning behind an interrupted coding session: the goal, safe next step, files changed, test evidence, rejected options, and risks are scattered across terminal scrollback, Git diffs, and model output. Git can show what changed but cannot reliably explain why it changed or enable a fresh Aider session to absorb a new constraint safely.

## Solution

Aider Continuum is a CLI-native integration that makes an Aider Session durable in Entire. `aider-entire` runs Aider in its normal terminal or scripted mode while isolating its histories and recording a privacy-preserving event journal. `entire-agent-aider` exposes that context to Entire Checkpoints. At an explicit Checkpoint Milestone, the developer receives a Continuity Brief containing the goal, redacted prompt digest, verified outcome, evidence, assumptions, failures, and open risks. A new Aider session can retrieve the brief, inspect evidence, pass the Impact Gate for a high-risk change, and continue only after an explicit developer instruction.

## User Stories

1. As an Aider user, I want to start a named isolated Aider Session, so that unrelated terminal work cannot contaminate its history.
2. As an Aider user, I want to keep using Aider's familiar interactive CLI, so that continuity support does not require a new editor or web application.
3. As an automation user, I want to run a one-shot Aider request through the same workflow, so that scripted and interactive work have consistent evidence.
4. As a developer, I want a redacted digest of each meaningful request, so that checkpoints retain intent without exposing sensitive prompt contents.
5. As a developer, I want changed files and relevant test outcomes captured for a session, so that I can distinguish an attempted edit from a verified result.
6. As a developer, I want to create a checkpoint only at a meaningful milestone, so that checkpoint history is concise and decision-rich.
7. As a developer returning after a break, I want a Continuity Brief from a new terminal session, so that I can reconstruct progress without the old terminal scrollback.
8. As a developer resuming work, I want the tool to wait for my instruction before changing code, so that recovered context never triggers an unsafe automatic retry.
9. As a developer planning a risky change, I want an Impact Gate backed by Entire Graph evidence, so that Aider receives a verified scope and test plan.
10. As a developer, I want to inspect the source or tests supporting each Graph finding, so that I can correct incomplete or uncertain Graph output.
11. As a developer, I want the final semantic-diff analysis and tests recorded, so that I can defend the submitted behavior to a reviewer.
12. As a developer affected by a new requirement, I want to resume from a pre-change checkpoint and record the revised decision, so that the curveball response preserves the original intent.
13. As a privacy-conscious developer, I want full prompts, model responses, and API keys excluded from briefs, Git, and checkpoint payloads, so that stored development context is safe to share.
14. As a developer using Groq, I want model selection to be runtime configuration, so that the integration does not break when model availability changes.
15. As a demo owner, I want actionable failure messages and a prepared fixture or recording for provider failures, so that an unavailable live service does not obscure the product's behavior.

## Implementation Decisions

- The product is terminal-first. No dashboard is required for the critical path.
- A small, protocol-first external-agent binary implements Entire protocol version 1 and only advertises capabilities that it implements and tests.
- A separate launcher owns the Aider process boundary. It creates a stable session identifier, an isolated directory, and explicit Aider history-file locations; the external-agent binary reads and serves those artifacts.
- The authoritative session record is an append-only JSONL journal of typed lifecycle events. Aider's native histories are retained as local raw evidence, not parsed as the primary contract.
- Each completed turn records a redacted prompt digest, model and edit configuration, elapsed time, process outcome, file-status delta, test command/result references, assumptions, rejected alternatives, and open risks.
- Groq is configured through Aider's native provider and an environment-held API key. Aider's OpenAI-compatible configuration remains a documented fallback. The selected model is configuration, not a hard-coded model identifier.
- Interactive Aider retains its streaming behavior. Deterministic scripted operations use message files and non-streaming mode where appropriate; secrets are never passed in command arguments.
- A Checkpoint Milestone is explicit rather than automatic. The generated Continuity Brief becomes checkpoint context and must link its Evidence Records.
- Resume reconstructs context and launches or prepares a fresh Aider Session but never automatically retries work or edits code.
- A declared high-risk change must pass the Impact Gate: Graph relationship or impact output is checked against source/tests, distilled into a verified brief, then supplied as context for the next developer-authorized Aider turn.
- A final semantic-diff analysis and verification outcome are recorded as submission evidence.
- Provider rate limits and failures use bounded retry where safe and an honest degraded mode. A response is never marked verified without a visible diff and relevant test outcome.

## Testing Decisions

- Test external protocol behavior: JSON input/output, capability declarations, session discovery, transcript chunk/reassembly, resume-command formatting, and error behavior. Do not test private parser or storage implementation details.
- Test launcher behavior with a fake Aider executable: isolated histories, typed journal events, redaction, file-delta capture, no accidental credential emission, and exact resume safety behavior.
- Test the high-level seam: a fresh process receives a Continuity Brief, requires explicit follow-up instruction, passes an Impact Gate, and records verified evidence after a change.
- Use protocol-compliance coverage for the external-agent contract; use lifecycle integration tests for Aider launch, checkpoint, and resume; use focused unit tests for journal and redaction rules.
- Use fixtures for Groq-provider failures and malformed Aider outputs. Live Groq calls are smoke checks, never the sole proof of correctness.

## Out of Scope

- A graphical dashboard, IDE extension, hosted multi-user service, cloud transcript storage, autonomous retries after resume, and unredacted prompt/model-response archiving.
- A promise that an Aider edit is correct without source inspection and tests.
- Support for every Aider mode or every third-party model in the hackathon release.

## Further Notes

- The demo narrative is continuity first: establish a verified baseline, checkpoint it, end the session, start fresh, recover the brief, process a changed constraint through the Impact Gate, and show the new tested checkpoint.
- Required Buildathon evidence includes the four checkpoint milestones, Graph lookup, risk impact analysis, final semantic diff, source/test verification, and a runnable fallback demo asset.
