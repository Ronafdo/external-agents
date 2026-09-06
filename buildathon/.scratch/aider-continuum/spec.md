## Problem Statement

Developers using Aider lose decision context when a coding session is interrupted. Git shows the diff but not the goal, verified result, assumptions, failed attempts, or risks a fresh session needs to continue safely.

## Solution

Aider Continuum is a CLI-native Entire integration for Aider. It records redacted, evidence-backed Continuity Briefs at explicit Checkpoint Milestones, lets a fresh Aider Session reconstruct context without automatic edits, and requires an Entire Graph Impact Gate before declared high-risk changes.

## User Stories

1. As an Aider user, I want isolated Aider Sessions, so that unrelated work cannot contaminate my history.
2. As an Aider user, I want to remain in Aider's normal CLI workflow, so that continuity requires no new editor or dashboard.
3. As a developer, I want redacted prompt digests and verified outcomes captured, so that checkpoints preserve intent without exposing sensitive content.
4. As a developer, I want changed-file and test Evidence Records, so that attempted changes cannot be confused with verified work.
5. As a developer, I want explicit Checkpoint Milestones, so that checkpoint history is concise and decision-rich.
6. As a developer returning after a break, I want a fresh session to retrieve a Continuity Brief, so that I can safely reconstruct prior work.
7. As a developer, I want resume to wait for my instruction, so that recovered context never triggers an unsafe automatic retry.
8. As a developer planning a high-risk change, I want a verified Graph Impact Gate, so that the next Aider turn has an accurate scope and test plan.
9. As a developer, I want final semantic-diff and test evidence, so that I can explain why the submitted behavior is trustworthy.
10. As a privacy-conscious developer, I want raw prompts, responses, and credentials excluded from checkpoint context, so that collaboration does not leak secrets.
11. As a Groq user, I want runtime model configuration, so that changing model availability does not require rebuilding the integration.
12. As a demo owner, I want credible provider-failure behavior and fixtures, so that a live-service failure does not conceal the product workflow.

## Implementation Decisions

- Pair a protocol-first `entire-agent-aider` external-agent binary with an `aider-entire` launcher that owns the Aider process boundary.
- Maintain an isolated session store and append-only typed journal as the canonical record; retain Aider histories as local raw evidence rather than a parsing contract.
- Store redacted digests, model/edit configuration, file deltas, test result references, assumptions, rejected options, failures, and open risks in every meaningful session record.
- Use Aider's native Groq provider with environment-held credentials; retain OpenAI-compatible Groq configuration as a documented fallback.
- Create checkpoints only at explicit Checkpoint Milestones. Resume prepares a new Aider Session from the Continuity Brief and waits for developer input.
- Require verified Entire Graph analysis before a declared high-risk change, then record final semantic-diff verification.
- Use bounded provider retry and clear degraded-mode reporting. Do not mark an outcome verified without a visible diff and relevant test result.

## Testing Decisions

- Test external protocol behavior through its public JSON and process contract.
- Test launcher behavior with a fake Aider executable, including isolation, redaction, evidence capture, and safe resume.
- Test the high-level continuity path: checkpoint, fresh session, explicit instruction, Impact Gate, edit, and verified evidence.
- Use fixtures for provider failures; live Groq calls are smoke tests, not the only correctness evidence.

## Out of Scope

No dashboard, IDE extension, hosted transcript store, automatic post-resume edits, unredacted checkpoint storage, or comprehensive support for every Aider mode/model in the hackathon release.

## Further Notes

The demo proves continuity first: checkpoint a verified baseline, close the session, resume fresh, apply the changed constraint through the Impact Gate, and checkpoint the tested result.
