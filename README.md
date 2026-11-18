# github-core-clone-01-v2

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/Yogii123-qw/github-core-clone-01-v2/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/Yogii123-qw/github-core-clone-01-v2/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

A standalone, GitHub-compatible git forge in one process: git smart HTTP, REST v3, GraphQL, and MCP over a single in-memory store, built as a statically linked Go binary launched from Node. No UI, no database, no git binary, no dependencies beyond the Go and Node standard libraries.

## Run it

```sh
FORGE_PORT=8081 node main.ts
```

---

<sub>Entered on [make.fun](https://make.fun/?ref=wdh6z73q&utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>

## Surfaces

| Route | What it is |
| --- | --- |
| `GET /healthz` | 200 `{"ok":true}` boot probe |
| `/<owner>/<repo>.git/...` | git smart HTTP: `info/refs`, `git-upload-pack` (protocol v2 and v0), `git-receive-pack` (v0) |
| `/api/v3/...` | GitHub REST v3: repositories, branches, commits, contents, labels, issues, comments, pull requests, merges |
| `POST /api/graphql` | GitHub GraphQL v4, including introspection, connections with cursor pagination, and mutations |
| `POST /mcp` | Model Context Protocol over streamable HTTP: `server/discover`, `initialize`, `tools/list`, `tools/call` |

## How it runs

`node main.ts` spawns the vendored `forge` binary next to it, forwards stdio, and propagates its exit code. The binary binds an ephemeral port, registers it with the suite at `$MAKEFUN_ENV_URL`, serves until the suite reports done, then exits 0. For local development only, `FORGE_PORT=8081 node main.ts` serves the forge standalone on that port (the trial always sets `MAKEFUN_ENV_URL`, so the standalone path never triggers there).

Rebuild the binary from source with `./build.sh` (needs a Go toolchain; the committed binary is what the trial runs).

## Layout

- `main.ts`: the launcher; it only spawns `./forge`.
- `forge`: the statically linked Linux amd64 binary, committed as vendored source.
- `src/main.go`: the entry <-> env lifecycle (bind, register, poll `/done`).
- `src/server.go`: the HTTP front door: routing, auth, body handling, lock discipline.
- `src/store.go`: the in-memory store and entity model.
- `src/gitobj.go`: git objects, pack codec (including deltas), traversal, pkt-lines.
- `src/gitproto.go`: smart-HTTP protocol handlers.
- `src/rest.go`: REST v3 serializers and the shared domain operations.
- `src/rest_handlers.go`: REST v3 routing and handlers.
- `src/graphql.go`, `src/graphql_schema.go`: GraphQL parser, schema, and executor.
- `src/mcp.go`: MCP tools over the same store.
- `src/jsonx.go`: JSON helpers matching `JSON.stringify` output.

## Notes

- Auth is the bearer token in `MAKEFUN_SHOP_TOKEN`, required on every route except `/healthz`.
- Timestamps are deterministic: a base time plus a per-store sequence, so two runs that see the same requests in the same order behave identically.
- Packfiles served to fetchers are never deltified; deflated object payloads and whole-pack responses are cached per repository.
- Read-only routes run concurrently under a read lock; mutating routes take the write lock.

MIT licensed.
