# Reviewed application network streams

Status: H08A development acceptance, 2026-11-03. No production enablement or
public protocol freeze is claimed.

## Decision

`network.stream` is a separate reviewed capability from `network.http`. It
permits ordinary HTTPS response streaming only:

- one exact reviewed origin, path prefix set and GET/POST method set;
- no WebSocket, CONNECT, protocol upgrade, custom tunnel, proxy or caller-selected
  dialer;
- an explicit response media-type set from `application/json-seq`,
  `application/x-ndjson` and `text/event-stream`;
- bounded request, idle time, total duration, total bytes, frame bytes and
  concurrent streams; and
- current Portal viewer, installation generation, release, policy revision,
  grant revision, controller epoch and credential binding checked again before
  every frame is released.

The stream grant cannot reuse a `network.http` policy. Install review records
`network_stream_policy` separately and the runner must advertise
`portal.http-stream-policy/1-dev.1`, a 4 MiB total stream, 32 KiB frames, a
30-second maximum duration and a 15-second maximum idle interval in its fresh
runtime profile.

## Bounds

| Resource | Development bound |
| --- | --- |
| Request body | policy-selected, at most 65,536 bytes |
| Total stream | policy-selected, at most 4 MiB |
| Frame | at most 32 KiB |
| Frames / online checks | at most 160 |
| Duration | at most 30 seconds |
| Idle interval | at most 15 seconds |
| Runner session | at most 35 seconds |
| Buffered response | unchanged 1 MiB `network.http` limit |

Paths, methods, media types, integer fields and enum values are canonicalized.
Noncanonical policy bytes, unknown fields, duplicate fields, unsafe hosts or
ports, ambiguous paths and unapproved response media types are refused before
execution.

## Portal to host

The session-only route is:

```text
POST /api/app-platform/installations/{installation}/network/stream
```

Its accepted request body is the same closed request shape as buffered networking.
A successful Portal response is `application/x-ndjson` with at most four frame
types:

```json
{"type":"start","status":200,"contentType":"application/x-ndjson"}
{"type":"chunk","sequence":1,"bodyBase64":"..."}
{"type":"end","sequence":1,"bytes":3,"sha256":"..."}
{"type":"error","code":"uncertain","retryable":false}
```

`start` is sent once and only for a 2xx upstream status. Chunk sequences start at
1 and cannot repeat, skip or exceed the frame ceiling. `end` is emitted only
after the exact byte count, frame count and SHA-256 match. A revocation,
disconnect, malformed frame, oversized frame, invalid media type or missing end
is uncertain; another request may be reviewed explicitly, but the same stream is
never replayed.

## Bridge and SDK

The application document never calls the Portal route. It uses:

```ts
const network = useAppNetwork();
await network.stream(
  { method: "GET", url: "https://approved.example/v1/events" },
  {
    onChunk(data, sequence) {
      // Consume the bounded byte frame.
    },
  },
);
```

The bridge uses separate `network.stream.request`, `.start`, `.chunk`, `.ack`,
`.cancel`, `.end` and `.error` messages. The host does not read the next chunk
until the application acknowledges the current chunk after its callback
resolves. This carries backpressure through host fetch, Portal response writing,
runner socket writing and the upstream response body.

Only `network.stream` authorizes the channel. `secrets.use` remains a separate
optional grant for a binding name; no credential value crosses the bridge.
Closing the app document, aborting the signal, replacing the presentation or
revoking authority rejects pending work as uncertain without retry.

## Verification

Focused tests cover:

- canonical stream policy parsing and exact grant/runtime-profile agreement;
- real TLS chunk delivery, total/frame limits and current checks before frames;
- revoke-before-dispatch and revoke-after-chunk behavior;
- upgrade, redirect, compression, idle-source and echo-policy refusal;
- separate signed command and challenge domains from buffered network/backend;
- runner stream request, ordered chunks, ack-gated writes, cancellation, missing
  end, duplicate sequence, digest drift and bounded malformed replies;
- Portal NDJSON writer, no-store admission and independent stream budget;
- host streaming-fetch parsing, end digest verification, backpressure, missing
  end, duplicate chunks and malformed/oversized frames; and
- Apps `useAppNetwork().stream()` capability, binary delivery, ordering and
  unavailable/credential behavior.

The full disposable Linux matrix passed in
[run 41706400426](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/41706400426)
with Portal `b7ce1d19` and Apps `b3a5d1b`. It exercised a real TLS upstream,
runner and Portal NDJSON delivery, the shared SDK in Chromium, app-ack
backpressure, and revocation before the first frame and after a released frame.
The artifact records `native_sdk_network_stream=true`; the required browser
marker is `SDK_NATIVE_BROWSER_NETWORK_STREAM`. The reduced native-only run
`41693464881` also passed. Exact evidence is in
[platform network stream](handoff/evidence/platform-network-stream/acceptance.json).

## Remaining gates

The public protocol remains experimental. Production grant issuance,
registry/auth exceptions, internet egress policy, SDK/protocol freeze and D6
approval are unchanged. React and Node MessagePort coverage remains development
protocol evidence, not a cross-browser production claim.
