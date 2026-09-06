# 01: Establish a protocol-recognized Aider Session

**What to build:** A developer can enable Entire for Aider and start one identifiable Aider Session that Entire recognizes, inspects, and associates with the project. The session contract works end to end with a representative Aider fixture and reports actionable errors without corrupting protocol output.

**Blocked by:** None (can start immediately).

**Status:** resolved

- [x] Entire discovers the Aider integration and its declared capabilities are protocol-compliant.
- [x] A developer can create and inspect one Aider Session through the supported Entire workflow.
- [x] Contract tests cover successful and invalid protocol interactions.

## Answer

Completed with `entire-agent-aider` and `aider-entire`. The module build, unit suite, shared protocol verifier, and fixture lifecycle verifier pass. The repository-wide e2e harness remains Windows-incompatible because existing adapters use Unix-only process-group APIs; this is a baseline platform limitation, not a failed Aider protocol check.
