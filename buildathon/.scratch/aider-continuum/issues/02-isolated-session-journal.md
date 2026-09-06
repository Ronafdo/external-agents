# 02: Capture an isolated, redacted Aider Session journal

**What to build:** A developer can run Aider through the CLI-native workflow and receive an isolated Aider Session whose journal captures redacted intent, lifecycle outcome, changed-file evidence, and test-result references without exposing prompts, responses, or credentials.

**Blocked by:** 01: Establish a protocol-recognized Aider Session.

**Status:** ready-for-agent

- [ ] Two sessions cannot mix their histories or Evidence Records.
- [ ] The journal captures a redacted digest, model configuration, outcome, and file/test evidence.
- [ ] Tests prove credentials and unredacted prompt content are excluded from persisted continuity context.
