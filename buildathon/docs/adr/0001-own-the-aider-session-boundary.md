# Own the Aider session boundary

The integration will pair a protocol-compliant `entire-agent-aider` binary with a CLI-native `aider-entire` launcher. The launcher, rather than Aider internals or terminal-stream scraping, owns an isolated session directory and an authoritative event journal so concurrent sessions, resume, checkpoints, and normal Aider interaction remain reliable and auditable.

## Considered Options

- Parse Aider's ANSI terminal output: rejected because streaming presentation is not a stable event contract.
- Embed Aider through its Python API: rejected because Aider does not promise compatibility for this integration surface.
