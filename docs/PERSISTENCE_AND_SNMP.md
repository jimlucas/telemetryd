# Persistence and future SNMP design

The current program intentionally keeps only current state in memory. This document describes the extension points already present and the invariants a durable or SNMP implementation must preserve.

## Persistence seam

`internal/ingest.BatchRecorder` receives normalized `model.NotificationBatch` values containing:

- stream/session identity;
- resolved BN identity and quality;
- collector receive time;
- original source timestamp nanoseconds;
- atomic flag and prefix;
- canonical updates and deletes;
- decoded values and Tarana hints;
- per-stream message sequence and process-wide order.

A recorder must treat the batch as immutable and return promptly. A production writer should enqueue into a bounded channel and expose queue depth, drops, retry count, and last successful commit.

Suggested implementations:

### Append-only WAL

Use length-prefixed protobuf, CBOR, or JSON records:

```text
magic/version
record length
CRC
normalized batch
```

Rotate by size/time, `fsync` according to the required durability policy, and replay in `ObservationOrder` after restart.

### Message bus

Publish one normalized batch per message to Kafka or NATS JetStream. A useful partition key is the resolved BN ID so all observations from one BN remain ordered within a partition.

### Database

Two logical models are useful:

1. Append-only observation/event table for audit and reconstruction.
2. Current-value table keyed by `(device_kind, device_id, canonical_path)` for fast current queries.

Do not store only the current-value table if post-incident reconstruction matters.

## Acknowledgement policy

The current server acknowledges after normalization/reconciliation and does not wait for durable storage. That keeps the listener independent of storage latency.

A durable deployment should make the policy explicit:

```text
memory-first:  ack after in-memory apply
queue-first:   ack after enqueue to a bounded durable producer
wal-first:     ack after local WAL append
commit-first:  ack only after remote database commit
```

`commit-first` can create BN backpressure and reconnect storms when the database is impaired. It should not be introduced accidentally by placing synchronous database writes in `HandleResponse`.

## Checkpointing current state

For fast restart, periodically checkpoint:

```text
format version
last replayed order
BN records and latest metrics
RN records and latest metrics
parent-candidate evidence
closed/open session summary as desired
stats baseline
SNMP index allocation map
```

Write a temporary file, `fsync`, and rename atomically. Replay WAL records newer than the checkpoint order.

## SNMP index stability

Current indices are positive 31-bit values derived from FNV-1a over:

```text
"bn:" + BN_ID
"rn:" + RN_ID
```

Hash collisions are resolved by linear probing in the process-local `indexOwners` map.

This gives deterministic indices in the normal no-collision case, but a strict SNMP contract requires persisting the final owner/index mapping. Otherwise, collision resolution can depend on discovery order after a restart.

## Suggested private MIB shape

Use an enterprise OID assigned to your organization. Do not ship a made-up enterprise number.

### Collector scalars

```text
collectorUptime
collectorActiveStreams
collectorBNCount
collectorRNCount
collectorMissingTimestampTotal
collectorSourceRegressionTotal
collectorDecodeErrorTotal
```

### BN table indexed by `bnIndex`

```text
bnID
bnHostname
bnMAC
bnStatusCode
bnStatusText
bnLastReceivedTicks
bnTimestampQuality
bnActiveStreams
bnReportedConnections
bnFreshRNCount
bnConnectionDelta
```

### RN table indexed by `rnIndex`

```text
rnID
rnParentBNIndex
rnParentBNID
rnHostname
rnMAC
rnStatusCode
rnStatusText
rnLastReceivedTicks
rnTimestampQuality
rnExplicitState
rnMetricCount
```

### Current metric table

A fully generic path table can be expensive for SNMP managers. Two options:

1. Define fixed Tarana KPI columns for the operational set you actually poll.
2. Expose a generic metric table indexed by `(deviceIndex, pathIndex)` and persist a path dictionary.

For Zabbix and most NMS workflows, fixed columns are easier and more stable:

```text
rnDLSNR
rnULSNR
rnPathLoss
rnRFRange
rnRadio0RX
rnRadio1RX
rnRadio2RX
rnRadio3RX
```

Each value should have a companion freshness/timestamp-quality column or use a documented sentinel for missing values. Do not return an old numeric value as though it were fresh without exposing age.

## SNMP implementation boundary

An SNMP agent should read immutable snapshots or call narrow store getters. It must not hold the state lock while encoding network responses.

Recommended package boundary:

```text
internal/snmp
  table builder
  OID registry
  scalar/table handlers
  snapshot refresh cache
```

Keep OID mapping out of the Tarana adapter. The adapter interprets telemetry; the SNMP layer presents reconciled state.

## Retention policy after persistence

Even with a database, keep the in-memory current-value model. Monitoring reads should not depend on database health or query latency. Persistence is for restart recovery, history, and audit—not a replacement for the reconciler's current state.
