# Protocol evidence and compatibility

Inspected public source pinned at [kallisti d8fd12d](https://github.com/kallisti-chat/kallisti/tree/d8fd12dd2a6f1483c8fdee187ebed827b3bd2efc) and [jukebox 59d63bb](https://github.com/kallisti-chat/kallisti-jukebox/tree/59d63bb12ebe9ac80d4cd5248374c2936e9bb6c0). Source was cloned read-only outside the deliverable; no generated protobuf/upstream implementation is copied.

| Contract | Source at pinned Kallisti revision |
|---|---|
| `/ws`: binary Mumble + JSON text; account lookup; bridge keepalive | `server/internal/bridge/bridge.go` |
| Bearer session token, token subprotocol, login token response | `server/internal/auth/auth.go` |
| Version type 0, Authenticate type 2 with username field 1 and opus field 5, ServerSync type 5 session field 1, UserState type 9 session field 1/user_id field 4 | `packages/protocol/proto/Mumble.proto`; jukebox `mumble.go` |
| Sidecar `ready.channels`, `uploadMax`, `maxUploads` | `server/internal/sidecar/resync.go` |
| 2000-rune text, 64-byte nonce, 5 req/s burst 10, upload token 5 minutes | `server/internal/sidecar/hub.go` constants |
| `send` + channel store ID + text + nonce + uploads `{key,description}`; acknowledgment `message` with nonce; error carries nonce | `server/internal/sidecar/hub.go` `post`, `deliver`, `sendError` and `nonce_test.go` |
| Verified registered sender ID presence, including zero | `server/internal/sidecar/hub.go` `wireMessage`, `toWireFor` |
| 24-hour nonce lookup keyed by registered author; deletion removes message-row dedup | `server/internal/store/store.go` `NonceTTL`, `MessageByNonce`; store nonce tests |
| Duplicate nonce lookup precedes upload attachment validation | `server/internal/sidecar/hub.go` `post` |
| One-use token request, attachment owner matching and at most ten uploads | `server/internal/sidecar/hub.go` upload handling/attachments |
| Raw HTTP upload, `X-Upload-Token`, URL-escaped `X-Filename`, JSON key/type result | `server/internal/files/files.go` `upload` |
| Unattached upload lifetime ten minutes, random-key public file URLs | `server/internal/files/files.go` |

Implementation sends Version/Authenticate on the shared WebSocket and waits for ServerSync, a registered self UserState, and JSON ready. No voice-channel join is necessary. It validates configured store IDs are visible. It serializes upload-token requests because those replies have no request ID. Server HTTP redirects are refused; the account bearer goes only to `/ws`; one-use token goes only to `/api/upload`. Feed/media clients are separate and credential-free. A monotonic 250ms request spacing stays below sidecar's request rate.

Old servers without nonce echoes are unsupported: response timeout closes the connection and leaves the durable job uncertain. There is no advertised nonce capability flag; the pinned baseline and required runtime smoke test are the compatibility contract. Files are immutable for an attempted send; there is no native Mumble fallback because TextMessage alone has no application acknowledgment. Password mode sends JSON `{name,password}` to `/api/login` with `X-Kallisti-Session: token`; an absent token or native-Mumble fallback is refused. Login HTTP redirects are disabled, responses/secret files are bounded, and session tokens remain in memory. Token-file mode is an explicit alternative. Discovery reads initial registered UserState IDs/names and ready channel IDs without a sidecar mutation.

The implementation uses protobuf wire helpers for just the minimal handshake fields and ignores unrelated binary events after ready.

Offline protocol fixtures test our adapter against this documented wire shape. They are not an execution of the upstream server. Actual pinned-server runtime testing remains unrun. Keep the upstream references when reviewing protocol changes.


Delivery isolation extension: upstream `hub.go` Handle sets answering to the send nonce, and `send` adds it to error events (including slow-mode/rate replies). The adapter correlates exact nonce before classifying scope. Exact `unknown channel`, `you may not use this channel's chat`, and `unknown upload` identify a destination/job-local rejection. Slow-mode classification additionally requires `about=slow-mode`, matching channel and an exact bounded seconds message (server slowmode.go). Account/server rate limiting, server storage errors, message-policy errors and unknown text remain global. Untrusted error messages are inspected only within 1024 encoded bytes and converted to fixed classes; neither raw text nor echoed composer content is logged/persisted. Mismatched nonce is ignored and cannot grant local isolation; timeout then closes the connection conservatively.
