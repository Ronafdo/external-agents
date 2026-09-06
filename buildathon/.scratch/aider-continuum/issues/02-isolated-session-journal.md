# 02: Capture an isolated, redacted Aider Session journal

**What to build:** A developer can run Aider through the CLI-native workflow and receive an isolated Aider Session whose journal captures redacted intent, lifecycle outcome, changed-file evidence, and test-result references without exposing prompts, responses, or credentials.

**Blocked by:** 01: Establish a protocol-recognized Aider Session.

**Status:** resolved

- [x] Two sessions cannot mix their histories or Evidence Records.
- [x] The journal captures a redacted digest, model configuration, outcome, and file/test evidence.
- [x] Tests prove credentials and unredacted prompt content are excluded from persisted continuity context.

## Answer

Implemented isolated per-session Aider history and canonical journals with digest-only
continuity records. Resume validation, history ownership checks, evidence fingerprints,
and a repository evidence lock prevent cross-session mixing. Tests cover redaction across
the journal, transcript, hooks, protocol reads, and notifications; credentials, raw prompts,
model responses, and test output are never persisted in continuity context.
