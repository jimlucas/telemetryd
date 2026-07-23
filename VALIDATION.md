# Validation record

Validation date: 2026-07-21

## Scope

This record covers the supplied source checkpoint, its pinned public Go dependencies, the embedded dashboard, and an end-to-end run with the included Tarana-shaped dial-out simulator. No real Tarana BN hardware or production TCS instance was available in the validation environment.

## Dependency-backed build verification

The repository was resolved with its actual pinned modules and an authentic checksum file:

```text
github.com/openconfig/gnmi       v0.14.1
google.golang.org/grpc           v1.69.2
google.golang.org/protobuf       v1.36.2
```

Commands and results:

```text
go mod tidy                       PASS
go mod verify                     PASS
go test ./...                     PASS
go vet ./...                      PASS
go test -race -p 1 ./...          PASS
make build                        PASS
node --check internal/httpapi/ui/app.js
                                  PASS
```

`make build` produced static `telemetryd` and `telemetrysim` binaries. The race run used one package at a time to stay within the validation runner’s process limits; all project packages passed.

## Automated coverage

Tests cover, among other behavior:

- source timestamp missing, invalid, future, and regressed classification;
- latest-receipt overwrite semantics;
- exact, repeated, and sender-reported duplicate accounting;
- deterministic newest-current-record selection and filtering;
- bounded diagnostic RN attention ordering;
- parent BN disconnect versus explicit RN delete/offline evidence;
- positive online evidence not masking a later parent-stream loss;
- stale RN while its parent remains healthy;
- one RN claimed by multiple healthy BNs;
- atomic subtree invalidation, subscription ownership, and reconnect behavior;
- BN identity quality and record merge;
- keyed-list path disambiguation and per-radio lookup;
- capacity rejection/eviction behavior;
- typed-value decoding and canonical path construction;
- dynamic gRPC bidi service descriptors and method parsing;
- invalid TLS/mTLS configuration;
- HTTP bearer-token enforcement and public health/UI shell paths;
- UI routing, security headers, recent-record validation, device pagination, and attention limits;
- Zabbix discovery, collector fields, status codes, and direct metric lookup.

## End-to-end capture run

The final binaries were run on loopback with:

```text
gRPC: 127.0.0.1:55051
HTTP: 127.0.0.1:58080
HTTP bearer token enabled
```

The simulator sent one BN and three RNs every 300 milliseconds while injecting:

- missing source timestamps;
- regressed source timestamps;
- duplicate observations.

Observed behavior:

- one active BN stream and three reconciled online RNs;
- newest retained BN/RN records returned from `/v1/recent`;
- missing/regressed timestamp and duplicate counters increased;
- `/v1/attention/rns` returned a bounded operator view;
- unauthenticated `/v1/summary` returned `401`;
- authenticated JSON endpoints returned valid data;
- `/`, `/ui/`, static assets, `/healthz`, and `/readyz` were reachable as designed;
- dashboard responses included the configured Content Security Policy and related hardening headers.

## Disconnect and reconnect reconciliation

After the simulator process was stopped:

```text
active streams:             1 -> 0
BN status:                  online_observed -> disconnected
three RN statuses:          online_observed -> unknown_upstream
retained current records:   remained available
```

This specifically verifies that fresh positive RN connection evidence does not mask loss of the only parent telemetry stream.

After reconnecting the same BN and RN identities:

```text
active streams:             0 -> 1
BN status:                  disconnected -> online_observed
RN statuses:                unknown_upstream -> online_observed
inventory:                  remained 1 BN / 3 RNs
```

No duplicate device inventory was created by reconnect.

## Dashboard verification

The HTTP behavior was verified directly against the live process with `curl`. The validation environment’s managed Chromium policy blocked direct loopback navigation, so visual rendering was performed in headless Chromium with API responses captured from that same live run and injected through a same-shape fetch harness.

The rendered console was checked for:

- live capture banner and KPI population;
- RN status and integrity panels;
- 26 populated current-record rows in the captured view;
- stream and RN attention tables;
- record-detail dialog interaction;
- token dialog interaction;
- no browser console errors;
- no JavaScript page errors.

The resulting screenshot is included as `docs/dashboard-preview.png`.

## Packaging checks

The final source archive is subject to:

- `gofmt` cleanliness;
- JavaScript syntax validation;
- dependency-backed tests, vet, race, and build;
- secret/credential pattern scan;
- generated `SHA256SUMS` for project files;
- ZIP central-directory integrity test;
- external SHA-256 checksum.

## Limitations and required field validation

The simulator verifies the implemented public request/ack shape and state semantics, but it cannot substitute for a real Tarana deployment. Before production qualification, validate with the exact BN firmware/TCS release:

- full gRPC method path;
- plaintext/TLS/mTLS behavior;
- incoming metadata names and identity quality;
- acknowledgement timing requirements;
- actual notification timestamp behavior;
- atomic/delete semantics;
- every observed keyed path and explicit RN state field;
- reconnect, replay, duplicate, and ordering behavior;
- telemetry cadence and load.

A production-scale replay and memory profile are also still required. Persistence across process restart and an SNMP agent are extension points, not features of this in-memory checkpoint.
