# entire-agent-aider

`entire-agent-aider` lets Entire identify and inspect sessions created by the
included `aider-entire` launcher. The launcher creates an isolated session at
`.entire/aider-sessions/<name>/`, records `events.jsonl`, and passes explicit
Aider history paths to the real CLI.

```sh
mise run build
cp entire-agent-aider aider-entire /usr/local/bin/
entire enable --agent aider
aider-entire --name checkout-fix --message "Fix the checkout validation"
```

For repeatable development or tests, `aider-entire --aider-bin /path/to/fake`
uses a fixture executable. Aider itself must be available on `PATH` for
`entire-agent-aider detect` to report present.
