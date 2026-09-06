# Aider Continuum

A checkpoint-native Aider workflow that preserves verified development intent so a new Aider session can safely continue interrupted work.

## Language

**Aider Session**:
One isolated invocation history and its durable context, identified independently of a terminal process.
_Avoid_: Chat, run

**Continuity Brief**:
A redacted, structured account of an Aider Session's goal, verified outcome, assumptions, failures, and open risks that a fresh session can use to resume work.
_Avoid_: Transcript, summary

**Evidence Record**:
A verifiable reference to source changes, test results, or Entire Graph findings that supports a Continuity Brief claim.
_Avoid_: Proof, log

**Checkpoint Milestone**:
An explicit, meaningful point at which a Continuity Brief is attached to an Entire Checkpoint.
_Avoid_: Auto-save, turn checkpoint

**Impact Gate**:
The required Graph analysis and verification step that precedes a declared high-risk change.
_Avoid_: Graph suggestion, optional analysis
