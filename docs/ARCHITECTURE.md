# Architecture and invariants

## Design goals

This service is optimized for operational truth rather than time-series convenience:

1. Preserve what arrived, when it arrived, and what the sender claimed about time.
2. Keep one current value per device/path for cheap monitoring queries.
3. Never infer a definitive RN outage from omission alone.
4. Distinguish child uncertainty from parent transport failure.
5. Keep the transport, vendor interpretation, state machine, and query surfaces replaceable.
6. Bound memory growth and make degraded behavior observable.

## Components

### `internal/dialout`

The transport layer dynamically registers one or more configurable bidirectional gRPC methods. Each stream:

- gets a random process-local stream ID;
- captures peer address, allowlisted incoming metadata, and optional TLS subject;
- receives `gnmi.SubscribeResponse` messages;
- sends a zero-field protobuf acknowledgement after each message;
- exposes connect/disconnect reasons and counters;
- limits message size and concurrent streams;
- uses HTTP/2 keepalive and connection-age controls;
- recovers a stream-handler panic without killing the process.

It has no Tarana path logic.

### `internal/ingest`

The ingestor gives every received response:

- collector receive time;
- per-stream message sequence;
- process-wide observation order.

It joins notification prefix and update/delete paths, canonicalizes path keys, decodes every standard `TypedValue` form, and calls the configured adapter. It does not discard malformed JSON or non-finite floats: it stores a degraded string representation and records a decode error.

A `BatchRecorder` may observe each normalized notification before it is applied to memory. Recorders must treat batches as immutable and must not block the stream for slow storage work.

### `internal/adapter/tarana`

The adapter is intentionally small. It answers only vendor-semantic questions:

- What is the best BN identity?
- Does this path belong to an RN, and what is the RN ID?
- Does a value carry a hostname, MAC address, active-connection count, or explicit operational state?
- Is a deleted path the keyed RN connection root?

Unknown paths still enter the generic latest-value map.

### `internal/state`

The state store is protected by an `RWMutex`. Writes are serialized so the process-wide observation order and all related indices remain consistent. Read APIs clone maps and composite values before releasing the lock.

Primary structures:

```text
bns[bnID]                         -> BN current state
rns[rnID]                         -> RN current state
rnsByBN[bnID][rnID]               -> parent index
sessions[streamID]                -> active/closed stream evidence
bn.Metrics[canonicalPath]         -> latest BN value
rn.Metrics[canonicalPath]         -> latest RN value
rn.ParentCandidates[bnID]         -> recent parent evidence
indexOwners[positive31BitIndex]   -> SNMP-index collision map
```

The store also provides bounded query views that do not add another source of truth:

- `Recent` selects the newest retained device/path values with a bounded heap; it is a view of the current-value maps, not history.
- `RNAttention` prioritizes diagnostic RN states without allocating a full-fleet snapshot.
- `Summary` computes fleet/status counters directly rather than cloning every metric for each dashboard poll.

### `internal/httpapi`

The HTTP layer exposes the embedded `/ui/` Capture Console, token-protected JSON APIs, Zabbix discovery/scalar reads, and Prometheus collector health. Static UI assets are compiled into the Go binary and use only same-origin APIs. The shell is public so it can load in a browser, while telemetry data remains protected by the configured bearer token.

## Core invariants

### Latest receipt wins

`ReceivedAt` and `ObservationOrder` define collector order. A later receipt overwrites an earlier value at the same device and canonical path, even when its source timestamp regresses.

This is not a claim that the later source sample is newer. It is the requested current-value/SNMP-style contract: “show the most recent value submitted to this collector.” The regression remains visible in `timestamp_quality` and counters.

### Source and receive time never collapse

The store keeps source and receive time in separate fields. A missing source timestamp remains `nil`/zero with quality `missing`; receive time is not substituted into the source field.

Timestamp classification is performed against configurable bounds:

```text
0                         -> missing
negative or before 2000   -> invalid_past
beyond receive+skew       -> future
less than prior path time -> regressed
otherwise                 -> source
```

The original nanosecond value is retained when present.

### Identity is data, not a socket address

A BN can first arrive under a peer-IP placeholder and later identify itself by metadata or gNMI target. A higher-quality identity merges the temporary record, its metrics, streams, and child-RN parent index into the authoritative record.

RN identity comes from the keyed connection list, never from hostname alone.

### Omission is not deletion

A regular notification updates only paths explicitly present. Everything else remains cached.

A delete removes the selected path subtree. A keyed connection-root delete additionally records an explicit RN offline state.

An atomic notification invalidates omitted paths only beneath its prefix and only among values last owned by the same logical subscription scope. The ingestor derives that scope from `gRPC method + subscription-name`, allowing a reconnect of the same subscription to replace its prior snapshot. When `subscription-name` is absent, scope falls back to the connection-specific stream ID. This prevents an unrelated duplicate or parallel subscription from erasing state it never owned. Atomic omission is recorded separately from an explicit delete and is not interpreted as definitive offline unless the operator opts into that behavior after confirming the sender contract.

### Parent failure changes child certainty

An RN can be `online_observed` only while its parent BN is healthy. When the parent stream closes or the BN becomes stale, the RN becomes `unknown_upstream`. This prevents one BN or transport failure from masquerading as many confirmed RN failures.

### Repeated values are still liveness evidence

A repeated value overwrites the prior receipt metadata and refreshes device liveness. It increments either:

- `exact_duplicates`: same value and same nonzero source timestamp;
- `repeated_values`: same value with a different or missing source timestamp.

Sender-provided `Update.duplicates` is tracked separately on each metric and as a collector-wide cumulative counter.

### Reads never expose mutable internals

JSON and API callers receive cloned metrics, maps, path elements, timestamps, and decoded composite values. A future output cannot race by mutating state it did not create.

## Status state machine

### BN

```text
no active stream                     -> disconnected
active stream + age > BN threshold   -> stale_unconfirmed
active stream + fresh notification   -> online_observed
```

### RN

Evaluation order:

```text
multiple recent healthy parent candidates     -> conflict
fresh explicit offline/delete evidence       -> offline_reported
parent unknown/disconnected/stale             -> unknown_upstream
fresh explicit online evidence                -> online_observed
fresh RN observation + healthy parent         -> online_observed
old RN observation + healthy parent           -> stale_unconfirmed
```

Definitive negative evidence is retained even when the parent later disappears. Positive evidence is different: a previously reported `connected=true` cannot prove current RN availability after the only reporting BN stream has closed, so parent health is evaluated before positive online evidence.

An ordinary omission is not part of the state transition table.

## Complexity

Typical write cost:

```text
one update                         O(1) average map operations
one delete                         O(metrics on that device)
atomic replacement                O(metrics under affected BN/RNs)
identity merge                    O(old BN metrics + old child RNs)
metric-cap eviction               O(metrics/device) per required eviction
```

Typical direct read cost:

```text
exact canonical path              O(1)
base path + key selectors         O(metrics/device)
device detail                     O(metrics/device) when metrics included
summary                           O(all devices), without metric cloning
recent current records            O(all retained metrics × log(limit))
RN attention                      O(all RNs × log(limit))
full snapshot / discovery         O(all devices and requested metrics)
```

Atomic processing uses the `rnsByBN` index and does not scan unrelated RNs.

## Adding another vendor

If the device sends the same message shape—`gnmi.SubscribeResponse` on a bidi stream with an empty acknowledgement—implement `adapter.Adapter` and select it in `cmd/telemetryd`.

A new adapter should have deterministic tests for:

- device identity precedence;
- child identity extraction;
- explicit state normalization;
- delete-root recognition;
- inventory hints.

If the service method or protobuf request differs, add another receiver package that emits `model.NotificationBatch`. Do not force vendor wire decoding into the state store.

## Adding persistence

A durable recorder should consume normalized immutable batches and append them before expensive transformation. Recommended order:

```text
receive -> assign receive order -> append WAL/queue -> apply current state
```

The current implementation logs recorder failure and continues in memory. Deployments that require “durable before ack” should add an explicit acknowledgement policy rather than silently making a database call inside the stream loop.

Recovery requires replaying normalized batches in observation order and restoring the device-to-SNMP-index allocation map. See `PERSISTENCE_AND_SNMP.md`.
