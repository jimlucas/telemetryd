# telemetryd checkpoint status

Checkpoint date: 2026-07-21  
Checkpoint state: feature-complete for the in-memory Tarana receiver generation; verified with the included fault-injecting simulator and real pinned Go dependencies.

## Ready now

The project builds two self-contained Go binaries:

```text
bin/telemetryd    Tarana-oriented dial-out receiver, reconciler, API, dashboard
bin/telemetrysim  compatible fault-injecting dial-out client
```

Start the service and open the embedded console:

```bash
export TELEMETRYD_TOKEN='replace-with-a-long-random-value'

./bin/telemetryd \
  --grpc-listen=:50051 \
  --http-listen=127.0.0.1:8080 \
  --http-token="$TELEMETRYD_TOKEN"
```

```text
http://127.0.0.1:8080/ui/
```

For remote browser access, bind `--http-listen=0.0.0.0:8080`, retain token authentication, and use `http://<collector-ip>:8080/ui/` through an appropriate management network or TLS reverse proxy.

## Implemented

### Tarana-compatible dial-out transport

- Default bidirectional gRPC method `/Nokia.SROS.DialoutTelemetry/Publish`.
- Receives standard `gnmi.SubscribeResponse`; returns one empty acknowledgement per message.
- Repeatable configurable method paths for future devices using the same request/ack shape.
- Stream IDs, peer identity, allowlisted metadata, TLS subject, message sequence, close reason, and session counters.
- TLS and optional/required client-certificate verification.
- Message-size, stream-count, HTTP/2 keepalive, and connection-age controls.
- Panic isolation and clean shutdown.

### Normalization and temporal evidence

- Canonical gNMI paths with all keyed-list selectors retained.
- All standard scalar `TypedValue` forms, JSON, JSON-IETF, bytes, leaf lists, `Any`, and opaque protobuf bytes.
- Distinct source timestamp, collector receive timestamp, stream message sequence, and process-wide observation order.
- Source-time quality: `source`, `missing`, `invalid_past`, `future`, or `regressed`.
- Exact duplicate, repeated value, sender-reported duplicate, value-change, and timestamp-regression counters.
- Missing source time stays missing; collector receive time is not relabeled as device time.

### In-memory SNMP-style current state

- One retained device record per BN/RN.
- One retained latest value per device and canonical path.
- Latest collector receipt overwrites the prior copy, while diagnostic counters remain cumulative.
- Non-atomic omission leaves cached values and inventory untouched.
- Explicit gNMI deletes remove the selected subtree.
- Atomic replacement is scoped by prefix and logical subscription ownership to prevent unrelated subscriptions from erasing each other.
- Bounded BN, RN, per-device metric, stream-session, and gRPC-message limits.
- Stable positive 31-bit in-process SNMP indices with collision handling.

### Tarana inventory and reconciliation

- BN identity preference and late identity merge.
- RN identity from keyed `connections/connection` paths, not hostname.
- BN/RN hostname, MAC, active-connection count, radio, SNR, path-loss, and range hints.
- RN parent-candidate tracking and multiple-parent conflict detection.
- Conservative status model: `online_observed`, `offline_reported`, `stale_unconfirmed`, `unknown_upstream`, `conflict`, `disconnected`, and `unknown`.
- Parent BN stream loss changes child RNs to `unknown_upstream` without deleting last-known values.
- Fresh positive `connected=true` evidence cannot mask a later parent-stream loss.
- Explicit offline/delete evidence remains definitive even when the parent subsequently disappears.

### Capture Console and query APIs

- Embedded, responsive `/ui/` operator console; no external assets, Node build, or CDN.
- Live capture status, rates, inventory, timestamp defects, duplicate evidence, RN status distribution, integrity warnings, RN attention queue, and stream sessions.
- Searchable/filterable newest-first current-record table with full metadata drill-down.
- Static shell is loadable without a token; data APIs remain bearer-token protected.
- Browser token kept only in `sessionStorage`.
- Content Security Policy and clickjacking/content-type/referrer/permissions headers.
- `/v1/recent` bounded latest-value view with device/path/status/time-quality/search filters.
- `/v1/attention/rns` bounded diagnostic-priority RN view for large fleets.
- `/v1/bns` and `/v1/rns` optional search, limit, and offset pagination.
- Summary implementation avoids cloning every device metric on routine dashboard polls.
- Full JSON snapshot, direct metric lookup, Zabbix scalar/discovery endpoints, and Prometheus collector/status metrics.

### Engineering and deployment material

- Unit and integration tests across transport, ingest, path handling, state, API, auth, UI, pagination, and reconciliation.
- Fault-injecting simulator for missing/regressed timestamps and duplicates.
- Authentic `go.sum` for pinned public modules.
- `Makefile` targets: `check`, `race`, `verify`, and `build`.
- GitHub Actions workflow for tests, vet, race detector, and builds.
- Dockerfile, Compose example, systemd unit, and environment example.
- Dashboard, architecture, Tarana paths, Zabbix, persistence/SNMP, source, and validation documentation.

## Verification completed

The final checkpoint was exercised against the real pinned dependencies, not local API stubs:

```text
go mod tidy                      PASS
go mod verify                    PASS
go test ./...                    PASS
go vet ./...                     PASS
go test -race -p 1 ./...         PASS
make build                       PASS
node --check UI JavaScript       PASS
```

End-to-end simulator verification covered:

1. Starting the gRPC receiver and token-protected HTTP service.
2. Connecting one BN and three RNs.
3. Injecting missing timestamps, regressed timestamps, and duplicate observations.
4. Confirming counters and current records through `/v1/summary` and `/v1/recent`.
5. Confirming unauthenticated data APIs return `401` while the dashboard shell and health endpoints load.
6. Closing the BN stream and confirming:
   - active streams became zero;
   - the BN became `disconnected`;
   - all three child RNs became `unknown_upstream`;
   - cached device/path values remained present.
7. Reconnecting the same simulated BN/RNs and confirming live statuses recovered without duplicate inventory.
8. Rendering the dashboard in headless Chromium with live captured API payloads, opening record/token dialogs, and observing no console or page errors.

See [VALIDATION.md](VALIDATION.md) for exact scope and limitations.

## Remaining production qualification

There is no known code or packaging blocker in this checkpoint. The following work requires the deployment environment or a real Tarana BN and therefore remains intentionally open:

1. **Real Tarana wire validation.** Confirm the public `gnmicc`/Tarana deployment uses the same method path, metadata, acknowledgement cadence, TLS mode, and standard `SubscribeResponse` payload on the exact installed firmware/TCS release.
2. **Real path corpus.** Capture representative BN/RN messages, compare every key spelling and explicit state/delete path with `docs/TARANA_PATHS.md`, and add fixtures for any variants.
3. **Cadence and threshold tuning.** Set BN/RN stale intervals from measured production telemetry cadence and reconnect behavior.
4. **Scale/load qualification.** Replay a production-sized corpus and choose memory limits, API polling intervals, and host sizing from measurements.
5. **Persistence policy.** The current generation is intentionally memory-only. Select WAL/queue/database durability and acknowledgement semantics before claiming recovery across process restarts.
6. **SNMP service.** Stable table indices and current-state access are present, but an SNMP agent/MIB is a future adapter. Zabbix can query the HTTP endpoints now.
7. **HTTP transport security.** For access outside a private management network, deploy HTTPS through a reverse proxy or add native HTTP TLS before production exposure.

A single imperfect telemetry source still cannot prove physical reality. `telemetryd` makes the evidence and uncertainty explicit; it does not invent certainty where Tarana did not send it.

## Design decisions to preserve

- Latest **receipt** wins the current-value slot; source time remains independent evidence.
- Omission is not deletion or offline.
- Missing source timestamps remain missing.
- Explicit negative evidence is stronger than upstream uncertainty; old positive evidence is not.
- Parent stream failure maps children to `unknown_upstream`.
- Repeated values refresh liveness even when downstream metric storage suppresses identical samples.
- The dashboard’s “recent records” are retained current values, not a hidden event-history buffer.
- Vendor semantics stay in an adapter; transport and core state remain reusable.
- Future persistence should consume normalized immutable batches rather than database writes being embedded in the gRPC handler.

## Resume commands

```bash
cd telemetryd

go mod download
make verify

export TELEMETRYD_TOKEN='development-token'
./bin/telemetryd \
  --grpc-listen=127.0.0.1:50051 \
  --http-listen=127.0.0.1:8080 \
  --http-token="$TELEMETRYD_TOKEN"
```

In another terminal:

```bash
./bin/telemetrysim \
  --server=127.0.0.1:50051 \
  --bn=BN-DEMO-001 \
  --rns=RN-DEMO-001,RN-DEMO-002,RN-DEMO-003 \
  --interval=1s \
  --missing-timestamp-every=3 \
  --regress-timestamp-every=5 \
  --duplicate-every=2
```

Open `http://127.0.0.1:8080/ui/`, select **Connect**, and enter `development-token`.

## Suggested next-turn objective

Use a sanitized raw/prototext capture or packet-level method/metadata summary from one real Tarana BN to validate the deployed wire contract and turn any discovered path variants into deterministic fixtures. Then run a sustained corpus replay to establish production memory and freshness settings before adding persistence or SNMP.

## 2026-07-22 CRT UI refresh

- Re-skinned the dashboard as a green-phosphor glass CRT console.
- Removed most of the generic startup-page feel in favor of a control-room display aesthetic.
- Kept the same routes and client behavior; this is a presentation-layer change only.
- Verified HTML parsing, JavaScript syntax, and CSS brace balance locally.
- Full binary rebuild was not re-run in this environment because outbound Go module fetches were blocked.
