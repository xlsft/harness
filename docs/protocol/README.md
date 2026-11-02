# harness protocols

All interaction between harness components goes over **gRPC-compatible RPC defined in protobuf**. The `.proto` files in [`/proto`](../../proto) are the contract and the source of truth. The TypeScript server and the Go packages share no code; they share generated code from these files.

## Services

| Service | Proto | Served by | Called by | Status |
|---|---|---|---|---|
| `harness.auth.v1.DeviceAuthService` | [auth.proto](../../proto/harness/auth/v1/auth.proto) | core | client | M0 |
| `harness.llm.v1.GatewayService` | [gateway.proto](../../proto/harness/llm/v1/gateway.proto), [content.proto](../../proto/harness/llm/v1/content.proto) | core | client | M0 |
| `harness.prompts.v1.PromptService` | [prompts.proto](../../proto/harness/prompts/v1/prompts.proto) | core | client | M0 |
| `harness.agent.v1.AgentService` | [agent.proto](../../proto/harness/agent/v1/agent.proto) | client | desktop / web / mobile frontends, relay | draft |
| `harness.sync.v1.SyncService` | [sync.proto](../../proto/harness/sync/v1/sync.proto) | core | client, frontends, `harness import claude` | draft (M1) |
| `harness.mcp.v1.McpService` | [mcp.proto](../../proto/harness/mcp/v1/mcp.proto) | core | client, frontends | draft (M2) |
| `harness.relay.v1.RelayService` | [relay.proto](../../proto/harness/relay/v1/relay.proto) | core | client (host side), web / mobile (viewer side) | draft (M2) |

Shared types: [errors.proto](../../proto/harness/common/v1/errors.proto), [scope.proto](../../proto/harness/common/v1/scope.proto) (`Scope` and the `project_key` format).

Server-side behavior that is not visible in the schema, such as provider translation, retries and capability negotiation, is described in [gateway.md](gateway.md). Open points of the tunnel and PTY data plane are listed in [tunnels.md](tunnels.md).

## Transport

- **[ConnectRPC](https://connectrpc.com).** Servers accept the gRPC, gRPC-Web and Connect protocols on the same endpoints. That gives:
  - Go clients use the Connect protocol by default over HTTP/1.1 or HTTP/2, on core's main port. Plain gRPC (`HARNESS_RPC_PROTOCOL=grpc`) needs HTTP/2 trailers, so core serves it on a separate h2c listener (`HARNESS_GRPC_PORT`), because Nitro's fetch-based responses cannot carry trailers;
  - browsers (web, mobile, the desktop webview) use Connect or gRPC-Web over HTTP/1.1 or HTTP/2;
  - everything works behind ordinary reverse proxies.
- **Libraries:**
  - Go: `connectrpc.com/connect` with `google.golang.org/protobuf`;
  - TypeScript: `@connectrpc/connect` with `@bufbuild/protobuf` (protobuf-es v2).
- **No bidirectional streaming.** Services use unary and server-streaming RPCs only, so every method works from a browser. Commands go as unary calls and live events as server streams.
- **Local transport.** Between a frontend and the local client, the same services run over a local socket, or over the stdio stream of an SSH session for remote machines.

## Conventions

- **Auth.** Every call except the unauthenticated `DeviceAuthService` methods carries `Authorization: Bearer <access token>`.
- **Client identity.** Clients send `User-Agent: harness-client/<version> (<os>; <arch>)`. The server rejects clients below its minimum version with `FAILED_PRECONDITION` + `ERROR_TYPE_UNSUPPORTED_CLIENT`.
- **Errors** are Connect/gRPC status codes with a `harness.common.v1.ErrorInfo` detail. The code mapping is in [errors.proto](../../proto/harness/common/v1/errors.proto).
- **Versioning.** The package version (`v1`) is the protocol version. Changes inside a version must stay backward compatible; `buf breaking` enforces this. Incompatible changes go to `v2`.
- **IDs** are opaque strings with a type prefix: `usr_`, `dev_`, `prv_`, `msg_`, `doc_`, `mcp_`, `mfl_` (MCP OAuth flow), `hst_` (relay host), `cmd_` (relay command), `tun_`. Session ids are UUID v4 (they double as Claude Code-compatible JSONL file names). Timestamps are RFC 3339 strings in UTC.
- **JSON payloads** that must round-trip exactly — tool inputs and JSON Schemas — are carried as JSON strings (`*_json` fields).

## Code generation

From the repository root:

```bash
buf lint proto
buf generate
```

Configuration lives in [`buf.gen.yaml`](../../buf.gen.yaml). Generated code is committed into each package:
- Go: `packages/client/gen`;
- TypeScript: `packages/core/src/gen`.

Each package stays buildable on its own.
