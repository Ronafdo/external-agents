# Entire × Aider (Groq) integration: research brief

Research date: 2026-09-06. Sources are primary: the supplied Buildathon guide, Entire's protocol specification/source repository, and official Aider/Groq documentation.

## What Track 3 requires

Track 3 is an integration, not a shell shortcut: it must bring Entire to a new agent or workflow, capture meaningful development context, and work in that agent's intended operating mode. Calling an Entire command from another interface alone is explicitly insufficient. The project must also use checkpoints and Graph evidence: graph lookup, relationship/impact analysis before a high-risk change, and final semantic-diff analysis, each verified against source/tests. [Participant Guide](../BTW%20Buildathon%202026%20-%20Participant%20Guide.pdf)

The strongest demo is therefore **Aider Continuum**: a real `entire-agent-aider` external-agent binary plus an `aider-entire` launcher. It gives every Aider run a durable, resumable session identity; connects prompt, model, response/context, changed files, tests and decision outcomes to Entire checkpoints; and lets a fresh agent recover a precise handoff after the noon constraint. This makes Entire essential rather than decorative.

## Non-negotiable protocol facts

- Entire discovers executables named `entire-agent-*` on `PATH`; `entire-agent-aider` registers as `aider`. The executable must answer `info` with protocol version 1. Each invocation is stateless, runs at `ENTIRE_REPO_ROOT`, and receives `ENTIRE_PROTOCOL_VERSION=1`. Structured payloads are JSON over stdin/stdout; failures are non-zero exits with stderr messages. [External Agent Plugin Protocol](https://github.com/entireio/cli/blob/main/docs/architecture/external-agent-protocol.md)
- `info`, `detect`, session ID/directory/file resolution, session read/write, transcript read/chunk/reassembly and resume-command formatting are required. Optional capabilities must only be declared when implemented; Entire will not call a subcommand for an omitted/false capability. [Protocol](https://github.com/entireio/cli/blob/main/docs/architecture/external-agent-protocol.md)
- A hooks-capable plugin must parse native payloads and install/uninstall/check hooks. A transcript analyzer supports incremental changed-file extraction by byte offset and prompt/summary extraction. A text generator can power Entire checkpoint summaries via `generate-text --model`, returning JSON text. [Protocol](https://github.com/entireio/cli/blob/main/docs/architecture/external-agent-protocol.md)

## Aider and Groq facts that affect the design

- Aider officially supports Groq with `GROQ_API_KEY` and the `groq/<model>` namespace; its documentation gives `groq/llama3-70b-8192` as an example and `--list-models groq/` to enumerate currently available models. Do not bake a dated model ID into the plugin; accept it as configuration and validate/list it at startup. [Aider Groq docs](https://aider.chat/docs/llms/groq.html)
- Groq also offers an OpenAI-compatible endpoint. Aider can target arbitrary compatible endpoints using `OPENAI_API_BASE`, `OPENAI_API_KEY`, and an `openai/<model>` name. Prefer Aider's native `groq/` provider where available; keep the compatible-endpoint route as a documented fallback. [Aider OpenAI-compatible APIs](https://aider.chat/docs/llms/openai-compat.html), [Groq compatibility docs](https://console.groq.com/docs/openai)
- Aider can run a single request non-interactively using `--message`/`--message-file`, then apply edits and exit. `--stream` defaults to enabled, while `--no-stream`, `--yes`, `--dry-run`, auto-commit controls, and history-file options make wrapper operation deterministic. [Aider scripting](https://aider.chat/docs/scripting.html)
- Aider persists chat history, input history, and optionally LLM history in configurable files; its source initializes a chat-history file and appends the user/assistant conversation. This is usable as the Entire transcript, but it is Markdown/plain text rather than a structured event log. [Aider `io.py`](https://github.com/Aider-AI/aider/blob/main/aider/io.py), [sample environment configuration](https://github.com/Aider-AI/aider/blob/main/aider/website/assets/sample.env)
- Default code mode edits; ask mode does not; architect mode makes two model calls (architect then editor). Architect mode may improve quality but costs/latency double. Aider recommends `editor-diff`/`editor-whole` for that mode. [Aider modes](https://aider.chat/docs/usage/modes.html)
- Edit formats are model-sensitive. Whole-file replacement is simple but can be slow/costly; SEARCH/REPLACE `diff` is efficient but depends on exact matching. Let Aider select settings for known models; record the selected model and edit format in session metadata and reject malformed edits rather than silently claiming success. [Aider edit formats](https://aider.chat/docs/more/edit-formats.html), [Aider model settings source](https://github.com/Aider-AI/aider/blob/main/aider/models.py)

## Recommended hackathon architecture

```text
Developer ──> aider-entire (one stable session ID) ──> Aider CLI ──> Groq
                   │                    │                │
                   │                    │                └─ edits / test commands
                   │                    ├─ isolated input, chat, LLM histories
                   │                    └─ JSONL event journal (authoritative)
                   v
             Entire hooks / checkpoint lifecycle <── entire-agent-aider
                   │                                      │
                   └── session store + transcript analyzer ┘
```

### Decisions

1. **Own the session boundary.** `aider-entire` generates a UUID and creates `.entire/aider-sessions/<id>/` with `input.history`, `chat.history.md`, `llm.history`, and `events.jsonl`. It launches Aider with its three explicit history paths and `--restore-chat-history` only for a resume. This prevents the default repository-wide Aider history from mixing concurrent sessions. The JSONL journal is the plugin's canonical transcript because it provides typed lifecycle records; Aider's history remains auditable raw evidence.
2. **Use a small, protocol-first Go binary.** Implement the required protocol commands before optional features. Start capabilities with `hooks: true`, `transcript_analyzer: true`, and `text_generator: true`; leave transcript preparation, token calculation, hook-response writing, and subagent support false until genuinely implemented. This minimizes protocol surface and makes compliance testable.
3. **Instrument the wrapper, not Aider internals.** Emit `SessionStart`, `TurnStart`, and `TurnEnd` around each invocation; snapshot `git status --porcelain` before/after to classify modified/new/deleted files; write prompt, selected model, exit status, elapsed time, test commands/results, and the raw Aider history reference into JSONL. Install an `aider` shell shim or have the launcher call an Entire hook endpoint so interactive use remains terminal-native. Never parse ANSI streaming text as the source of truth.
4. **Respect streaming.** For human interactive runs, inherit Aider's streaming default and tee only raw history/journal events. For checkpoint summarization or scripted execution, use `--no-stream` plus `--message-file`, yielding predictable capture and no secret-bearing prompt on the command line. Do not put `GROQ_API_KEY` in argv, logs, checkpoints, or `BUILDATHON.md`.
5. **Make checkpoints decision-quality.** At every meaningful milestone, store: goal, prompt digest, model/edit mode, files changed, test proof, assumptions, rejected alternatives, failure/retry evidence, and open risks. The pre-noon checkpoint must be enough for a new launcher session to reconstruct intent without old terminal scrollback.
6. **Make Graph change behavior, not presentation.** Before the selected risky change, run a Graph relationship/impact query, write the result and source/test verification to the Aider session journal, then pass a concise verified impact brief into the next Aider prompt. At the end, store the semantic-diff result and verification. The demo should visibly show an Aider prompt being narrowed or a test plan being selected because of Graph evidence.
7. **Use safe, one-day defaults.** Default to code mode with Aider-selected format and `--no-auto-commits`; expose architect mode as an opt-in "reviewed plan then edit" button/command for a small high-value change. Include retries with bounded exponential backoff for Groq 429/5xx, a clearly labelled offline/mock demo, and a test-first dry-run mode. Do not claim full recovery from a partially written history file: use atomic JSONL append/flush and report the gap.

## Narrow delivery sequence

1. Demonstrate `entire-agent-aider info`, `detect`, one new session, one prompt, extracted changed files, and a checkpoint.
2. Add robust resume: new shell/session reads the checkpoint and runs `aider-entire resume <id>` with the dedicated history paths.
3. Add visible Graph-informed impact brief and final semantic-diff evidence.
4. Add `generate-text` backed by the configured Aider/Groq model only if it is reliable; otherwise omit that capability and summarize through captured Aider context in the product UI.

## Risks and honest limits

- The protocol is strict about stdout JSON/raw bytes; diagnostic output must stay on stderr. Invalid `info` means the plugin is silently skipped at discovery. [Protocol](https://github.com/entireio/cli/blob/main/docs/architecture/external-agent-protocol.md)
- Aider's Python scripting API is explicitly unsupported for compatibility guarantees. Prefer its documented CLI for the integration's execution path. [Aider scripting](https://aider.chat/docs/scripting.html)
- Model availability/rate limits are external and mutable. Read the configured model at runtime, expose the failure, and preserve a demo recording/test fixture rather than fabricating a successful Groq response.
- Aider edit outputs are not proof that a change is correct. Require a git diff plus relevant test command/result before marking a turn verified.
