# CPA ch-relay Timeout and Stream EOF Fix Report

Date: 2026-10-17  
Target: `192.168.8.1`  
Service: CPA `8319` with local `ch-relay` on `127.0.0.1:11500`

## Symptoms

The following errors occurred while sending large Responses payloads:

```text
stream disconnected before completion: Transport error: network error: error decoding response body
stream disconnected before completion: unexpected EOF
stream disconnected before completion: host_call_failed: execute host http request: Post "http://127.0.0.1:11500/v1/responses": dial tcp 127.0.0.1:11500: connect: connection refused
```

The workload used large repeated filler payloads to burn input tokens. A 400-repeat payload produced HTTP 408 timeout behavior. Smaller payloads completed more reliably.

## Root causes

### 1. Relay first-byte timeout

The router script `/root/.ch-relay/run` configured:

```text
CH_UPSTREAM_HDR_MS=45000
CH_NO_FIRST_BYTE_MS=60000
```

Large prompts could exceed the 45-second upstream-header/TTFB window. The relay then terminated the request before the upstream response started, producing a timeout or incomplete stream.

### 2. Overly aggressive relay watchdog

The original `/root/.ch-relay/admin-watch.sh` restarted the relay after six failed health checks, with a three-second curl timeout and five-second polling interval. This allowed roughly 48 seconds of admin endpoint unavailability before killing `ch-relay`.

Router evidence showed:

```text
admin health failed (6)
admin hung, restarting ch-relay
Terminated
```

Any active stream was then closed, causing `unexpected EOF`.

### 3. Local relay downtime

During relay restart, CPA received:

```text
dial tcp 127.0.0.1:11500: connect: connection refused
```

This error means the local relay process was not listening at that moment. It is distinct from an upstream response decoding error.

## Changes applied on the router

### `/root/.ch-relay/run`

Changed:

```text
CH_UPSTREAM_HDR_MS=45000  -> 180000
CH_NO_FIRST_BYTE_MS=60000 -> 180000
```

The other relay stall and heartbeat settings were preserved:

```text
CH_UPSTREAM_IDLE_MS=180000
CH_POSTCALL_STALL_MS=180000
CH_CALL_ARG_STALL_MS=180000
CH_CALL_HB_MS=8000
CH_CALL_HB_EVERY_MS=5000
```

### `/root/.ch-relay/admin-watch.sh`

Changed the health request timeout from `3s` to `10s` and restart threshold from `6` to `12` failures.

The watchdog was then improved to check both endpoints:

```text
http://127.0.0.1:11504/healthz
http://127.0.0.1:11500/v1/models
```

It resets the failure counter when either endpoint is alive. Therefore, a slow admin endpoint does not kill a relay whose data endpoint is still serving traffic.

## Backups created

```text
/root/.ch-relay/run.bak-pre-timeout-fix-20261017
/root/.ch-relay/admin-watch.sh.bak-pre-timeout-fix-20261017
/root/.ch-relay/admin-watch.sh.bak-pre-data-health-20261017
```

The data-health backup was created during an intermediate edit attempt; the final watcher was restored from the known-good timeout-fix backup and then updated with the data-endpoint check.

## Validation

### Relay process and ports

Validated on the router:

```text
127.0.0.1:11500 -> ch-relay.direct listening
127.0.0.1:11504 -> ch-relay.direct listening
11504/healthz    -> HTTP 200 / {"ok":true}
11500/v1/models  -> HTTP 401 without authentication
```

HTTP 401 from `/v1/models` is expected without credentials and confirms that the data endpoint is alive.

### Large requests after timeout change

Relay logs recorded successful large requests:

```text
HTTP 200, input 163765 tokens
HTTP 200, input 167178 tokens
HTTP 200, input 169292 tokens
HTTP 200, input 172422 tokens
HTTP 200, input 178891 tokens
```

One 75-second request also completed with HTTP 200 after the relay timeout was raised.

### Repository validation

The relay-related tests passed:

```text
go test ./internal/pluginhost
go test ./sdk/cliproxy/auth
go test ./examples/plugin/ch-relay/go
go test ./examples/plugin/ch-relay-vippro/go
go build -o /tmp/cli-proxy-api-check ./cmd/server
```

A full repository test run also exposed one unrelated flaky concurrent test under `sdk/cliproxy`; running that test alone passed. No source code change was made for that unrelated test.

## Operational guidance

The relay timeout is now 180 seconds, but external gateways can still impose their own request limits. Keep individual requests below approximately 60–90 seconds when possible.

For a 100K-token workload:

1. Probe smaller payloads first, such as 50, 100, 150, 200, and 300 repeats.
2. Select the smallest stable payload with TTFB comfortably below the external gateway limit.
3. Use streaming for long output.
4. Fan out gradually, starting around 4–8 concurrent requests.
5. Increase concurrency only after checking 429 responses, relay health, CPU, memory, and completed usage events.

Streaming cannot solve a timeout that occurs before the first upstream byte. Smaller prompts reduce that risk; streaming mainly protects the response phase after TTFB.

## Current status

The local relay is listening and healthy. The original 45-second first-byte timeout and the watchdog behavior that caused avoidable stream termination have been corrected on the router. If the issue recurs, collect the exact timestamp and compare it with `logread` entries for `admin and data health failed`, `restarting ch-relay`, `408`, `499`, and the corresponding `share` request.
