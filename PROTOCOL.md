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

Offline protocol fixtures test our adapter against this documented wire shape. They are not an execution of the upstream server. A partial live check against an owner-run server passed on 2026-10-30 (see VERIFICATION.md); see the upstream review below for what the reported version does and does not identify; testing against the pinned revision itself remains unrun. Keep the upstream references when reviewing protocol changes.


Delivery isolation extension: upstream `hub.go` Handle sets answering to the send nonce, and `send` adds it to error events (including slow-mode/rate replies). The adapter correlates exact nonce before classifying scope. Exact `unknown channel`, `you may not use this channel's chat`, and `unknown upload` identify a destination/job-local rejection. Slow-mode classification additionally requires `about=slow-mode`, matching channel and an exact bounded seconds message (server slowmode.go). Account/server rate limiting, server storage errors, message-policy errors and unknown text remain global. Untrusted error messages are inspected only within 1024 encoded bytes and converted to fixed classes; neither raw text nor echoed composer content is logged/persisted. Mismatched nonce is ignored and cannot grant local isolation; timeout then closes the connection conservatively.

## Upstream review through Kallisti v0.11.0 — 2026-10-30

Reviewed the canonical repository's history from the pinned `d8fd12d` to tag
`v0.11.0` (`1c201c1`, 238 commits), plus the 57 commits from tag `v0.10.0`
(`7428e2d`) to the pin, for the files in the contract table above. The pin
stays at `d8fd12d`; nothing below has been run against a live v0.11.0 server.

Unchanged from the pin through v0.11.0:

- `send` with channel, text, nonce and `uploads` `{key,description}`; the
  sender's `message` copy carries the nonce; errors carry it via `answering`.
- 64 KiB sidecar frames, at most 10 uploads, `ready.uploadMax` and
  `ready.maxUploads`, 24-hour `NonceTTL`, and `MessageByNonce` dedup.
- `/api/upload` with `X-Upload-Token` and `X-Filename`; account upload tokens
  are still redeemed there. New logo tokens are refused by that endpoint.
- The Mumble protobuf definitions and the Version, Authenticate, ServerSync and
  UserState handshake.

Changed, and how it affects the bot:

- **Ended sessions are refused.** A WebSocket bearer token that names no
  session (expired, logged out, revoked by a password change) is now closed
  with `session expired` (policy violation) instead of falling through to the
  guest path. The bot still treats this as a failed connection. Password mode
  logs in again after 29 days, ahead of the server's 30-day session lifetime,
  but a revoked session still needs a restart, as documented.
- **Login from other sites is guarded.** `/api/login` from an Origin that is not
  the server's own site gets a per-name attempt limit, and a name with no web
  account gets `403`. The bot sends the configured `origin`; set it to the
  server's own site.
- **Guests only on the server's own site.** It does not affect the bot, which
  connects with an account token.
- Unrelated additions (server logo, home servers, OAuth, private categories,
  microphone info, connection lists) add request types and `ready` fields
  that the bot ignores.

Tag `v0.10.0` predates the pin and lacks multi-file sends: `send` takes a single
`upload` key and `ready` has no `maxUploads`, so the bot attaches no images
there (text only), and sidecar frames are limited to 16 KiB. The bot's largest
`send` frame (2,000 runes, each at most 6 bytes when JSON-encoded, plus fixed
upload descriptions) fits within that limit. The owner's server reports
version 0.10.0 but delivered image attachments in the live checks, so it runs a
build after the multi-file commits; upstream kept the 0.10.0 version string
until `v0.11.0`. The server exposes no build commit, so its exact revision
comes from the deployment, not from the bot.
