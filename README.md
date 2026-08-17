# agent-guard

A circuit breaker for AI agent sessions. It sits between your agent (Claude Code, or anything using the Anthropic API) and the real API, watches for signs the agent is stuck in a loop, and kills it before it burns your budget or your API credits.

## Why

Agent loops happen. A retry that never backs off, a tool call that keeps failing the same way, an agent stuck re-reading the same file — and by the time anyone notices, it's made thousands of identical API calls overnight. Observability tools will show you the trace *after* the damage is done. `agent-guard` stops it while it's happening.

## How it works

`agent-guard` wraps the command you'd normally run, spawns it as a child process, and quietly points its API traffic through a local proxy:

```
agent-guard claude -p "your prompt"
       │
       ├── starts a local proxy
       ├── runs `claude` as a child process, with its API traffic routed through the proxy
       └── watches every request/response for repeated tool calls or excessive runtime
              → if it trips, the agent process is killed immediately
```

It inspects real API traffic — actual tool calls and their arguments, not just printed text — so it can tell "the agent called the same tool with the same arguments 4 times in a row" apart from coincidental text repetition.

## Install

```bash
git clone <your-repo-url>
cd agent-guard
go build -o agent-guard.exe .
```

Requires [Go](https://go.dev/dl/) 1.21+.

## Usage

```bash
agent-guard [flags] <command> [args...]
```

Example — wrap a Claude Code session:
```bash
agent-guard claude -p "refactor this function"
```

### Flags

| Flag | Default | What it does |
|---|---|---|
| `-timeout` | `60s` | Kill the process if it runs longer than this |
| `-window` | `15` | How many recent output lines to remember for text-based repeat detection |
| `-threshold` | `6` | How many times an identical output line can repeat before tripping |
| `-tool-window` | `10` | How many recent tool calls to remember for repeat detection |
| `-tool-threshold` | `4` | How many times an identical tool call (same tool, same arguments) can repeat before tripping |
| `-port` | `8080` | Local port the proxy listens on |
| `-target` | `https://api.anthropic.com` | The real API the proxy forwards to |

Example with custom thresholds:
```bash
agent-guard -timeout=10m -tool-threshold=3 claude -p "fix the failing tests"
```

## What it catches

- **Repeated tool calls** — the same tool called with the same arguments too many times in a row (the most reliable signal — based on real structured API data, not printed text)
- **Repeated output lines** — the same line of printed output repeating (a fallback for agents/tools that don't go through the proxy for some reason)
- **Runaway wall-clock time** — the process simply running longer than expected, regardless of what it's doing

## Limitations

- Requires the wrapped tool to run in non-interactive/prompt mode (e.g. Claude Code's `-p` flag) rather than a fully interactive session, since output is piped rather than connected to a real terminal.
- The proxy currently buffers the full response before relaying it, rather than passing streaming output through live — functionally correct, but not as snappy as talking to the API directly.
- Detection is currently exact-match only (same tool, same arguments) — an agent looping with slightly varied arguments each time won't yet trip the breaker.

## License

MIT — see [LICENSE](LICENSE).