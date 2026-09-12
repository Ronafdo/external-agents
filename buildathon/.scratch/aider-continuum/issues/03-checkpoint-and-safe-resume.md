# 03: Create a Continuity Brief and safely resume it

**What to build:** At a Checkpoint Milestone, a developer can attach a decision-rich Continuity Brief to an Entire Checkpoint. From a fresh terminal, they can reconstruct the Aider Session from that brief while the workflow waits for their explicit next instruction before editing code.

**Blocked by:** 02: Capture an isolated, redacted Aider Session journal.

**Status:** resolved

- [x] A checkpoint includes goal, verified outcome, Evidence Records, assumptions, failures, and open risks.
- [x] A fresh session retrieves and presents the Continuity Brief without relying on prior terminal scrollback.
- [x] Automated coverage proves resume does not automatically retry or mutate code.

## Answer

Implemented an explicit Checkpoint Milestone with a separate, redacted,
content-addressed carrier session. Its binding manifest uses redaction-
invariant structural `*_id` values for the source session, source Brief payload
hash excluding `milestone_session_id`, and source evidence hash; those values
derive the carrier ID. Entire may redact the carrier's human-readable Brief
prose during attach or restore, so that prose is advisory rather than
byte-for-byte evidence. Source-local Brief evidence remains strictly validated
against the canonical source journal. When Entire is enabled, the carrier is
persisted through `entire session attach <carrier> --agent aider`; no synthetic
lifecycle event, automatic Aider retry, code mutation, test rerun, or forced
Git amend occurs. The Continuity Brief publishes its content-addressed
`milestone_session_id`, which is the exact recovery ID for a restored carrier;
the launcher does not scan carrier directories to resolve an original source
session ID. Attachment runs with noninteractive Git; if Entire provides an
`Entire-Checkpoint:` manual trailer, the launcher sends it to stderr for the
developer to apply.

Publication uses a private receipt that transitions `prepared` → `attaching` →
`attached`. Only a confirmed pre-launch/no-write failure returns to `prepared`
for an explicit retry. Any other started attach without confirmed success is
ambiguous. If a crash leaves the receipt missing, malformed, or `attaching`,
the launcher reconciles only with positive proof from
`entire session info <carrier> --json` and, when needed,
`entire checkpoint list --session <carrier> --json`. An empty or failed probe
does not prove that attachment did not happen; the launcher reports unknown
state rather than issuing a duplicate attach.

Validated with the Aider module unit suite, build, shared external-agent
protocol verifier, and fixture workflow. The repository-wide e2e harness is
currently Windows-incompatible before it reaches Aider because existing agent
adapters use Unix-only process-group APIs (`Setpgid` and `syscall.Kill`).
