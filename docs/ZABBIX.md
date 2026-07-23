# Zabbix integration

`telemetryd` is designed to be queried by Zabbix HTTP agent items. The service exposes low-level-discovery payloads and scalar endpoints so Zabbix does not have to parse the full in-memory snapshot for every item.

## Connectivity

The Zabbix server or proxy must be able to reach the HTTP listener. The default listener is loopback-only:

```text
127.0.0.1:8080
```

Either run the relevant Zabbix proxy on the same host or bind `telemetryd` to a protected management address:

```bash
telemetryd --http-listen=10.0.20.15:8080 --http-token='...'
```

For every HTTP agent item, add one of these headers:

```text
Authorization: Bearer {$TELEMETRYD.TOKEN}
```

or:

```text
X-Telemetry-Token: {$TELEMETRYD.TOKEN}
```

Use a Zabbix secret macro for the token. Do not put it directly in an item URL.

## Discovery endpoints

BN discovery:

```text
GET http://collector:8080/zabbix/discovery/bns
```

Macros:

```text
{#BNID}
{#BNHOSTNAME}
{#BNSTATUS}
{#BNMAC}
{#SNMPINDEX}
```

RN discovery:

```text
GET http://collector:8080/zabbix/discovery/rns
```

Optional parent filter:

```text
GET http://collector:8080/zabbix/discovery/rns?bn={URL-ENCODED-BN-ID}
```

Macros:

```text
{#RNID}
{#RNHOSTNAME}
{#BNID}
{#RNSTATUS}
{#RNMAC}
{#SNMPINDEX}
```

The response uses the long-supported `{"data":[...]}` discovery wrapper.

Recommended discovery interval is several minutes. Discovery is inventory, not a heartbeat.

## Collector fields

Endpoint:

```text
/zabbix/collector?field=<field>
```

This endpoint reads process-wide counts and ingestion diagnostics without building a full BN/RN snapshot. Supported fields are:

```text
uptime_seconds
bn_count
rn_count
active_streams
messages
notifications
updates
deletes
atomic_notifications
atomic_removals
deleted_metrics
exact_duplicates
repeated_values
reported_duplicates
value_changes
source_regressions
missing_source_timestamp
invalid_source_timestamp
decode_errors
protocol_errors
rejected_devices
identity_merges
evicted_metrics
opened_streams
closed_streams
sync_responses
```

Examples:

```text
http://collector:8080/zabbix/collector?field=active_streams
http://collector:8080/zabbix/collector?field=missing_source_timestamp
http://collector:8080/zabbix/collector?field=source_regressions
```

Useful collector-level triggers include active streams dropping to zero, protocol/decode errors increasing, rejected devices increasing, and timestamp-quality counters increasing unexpectedly. Counter deltas should be calculated by Zabbix rather than treating the cumulative value as an instantaneous rate.

## Scalar device fields

Endpoint:

```text
/zabbix/value?kind=bn&id={#BNID}&field=<field>
/zabbix/value?kind=rn&id={#RNID}&field=<field>
```

Common fields:

| Field | Result |
|---|---|
| `status` | Reconciled status text. |
| `status_code` | Numeric status: offline/disconnected 0, online 1, stale 2, upstream unknown 3, conflict 4, unknown 5. |
| `status_reason` | Human-readable explanation. |
| `available` | `1` only for `online_observed`; otherwise `0`. |
| `known_offline` | `1` only for explicit RN offline or disconnected BN. |
| `age_seconds` | Seconds since the last collector receipt for the device. |
| `first_seen_unix` | First in-process observation time. |
| `last_seen_unix` | Last collector receipt time. |
| `last_source_timestamp_unix` | Latest submitted source timestamp, or `0`. |
| `source_timestamp_present` | `1` when a source timestamp exists. |
| `timestamp_quality` | `source`, `missing`, `invalid_past`, `future`, or `regressed`. |
| `hostname` | Last-known hostname. |
| `mac_address` | Last-known MAC address. |
| `snmp_index` | Positive in-process table index reserved for future SNMP. |
| `metric_count` | Current latest-value path count. |

BN-only fields:

| Field | Result |
|---|---|
| `identity_quality` | Current Tarana resolution quality: `peer`, `metadata`, or `target` (`serial` is reserved for future adapters). |
| `active_streams` | Current active dial-out streams for the BN. |
| `fresh_rn_count` | Child RNs currently classified `online_observed`. |
| `reported_active_connections` | Tarana BN-reported active count, or `-1` if absent. |
| `connection_count_delta` | Reported active count minus freshly observed child RN count. |

RN-only fields:

| Field | Result |
|---|---|
| `parent_bn_id` | Current parent BN identity. |
| `parent_conflict_count` | Number of retained recent parent candidates. |
| `explicit_state` | Last explicit online/offline state, if any. |
| `last_delete_unix` | Last gNMI delete affecting the RN, or `0`. |
| `last_atomic_omission_unix` | Last atomic invalidation affecting the RN, or `0`. |

Example item URL:

```text
http://collector:8080/zabbix/value?kind=rn&id={#RNID}&field=status_code
```

Zabbix HTTP agent value type: `Numeric (unsigned)`.

## Metric values

Endpoint:

```text
/zabbix/value?kind=<bn|rn>&id=<ID>&path=<PATH>&attribute=<ATTRIBUTE>
```

Default attribute is `value_text`.

Metric attributes:

```text
value_text, value_type,
received_unix, received_unix_ms,
source_timestamp_unix, source_timestamp_ns,
age_seconds, source_age_seconds,
timestamp_quality,
samples, value_changes,
exact_duplicates, repeated_values,
source_regressions, reported_duplicates,
stream_id, scope_id, path, base_path,
first_seen_unix, changed_unix,
message_sequence, observation_order,
source_bn_id
```

Example RN DL-SNR item prototype:

```text
URL:
http://collector:8080/zabbix/value?kind=rn&id={#RNID}&path=/connections/connection/state/dl-snr&attribute=value_text

Type of information:
Numeric (float)
```

The key-free base path resolves only when exactly one current metric matches it. This avoids silently returning the wrong radio/list entry.

For a keyed path, add selectors:

```text
http://collector:8080/zabbix/value?kind=rn&id={#RNID}&path=/connections/connection/radios/radio/state/rx-signal-level/avg&key.radio_id=0&attribute=value_text
```

Zabbix will URL-encode the complete URL when configured normally. When constructing URLs manually, encode RN/BN IDs and paths.

## Recommended item prototypes

For each discovered RN:

```text
telemetryd.rn.status_code[{#RNID}]
telemetryd.rn.known_offline[{#RNID}]
telemetryd.rn.age[{#RNID}]
telemetryd.rn.timestamp_quality[{#RNID}]
telemetryd.rn.parent[{#RNID}]
telemetryd.rn.dl_snr[{#RNID}]
telemetryd.rn.ul_snr[{#RNID}]
telemetryd.rn.path_loss[{#RNID}]
telemetryd.rn.rf_range[{#RNID}]
```

For each discovered BN:

```text
telemetryd.bn.status_code[{#BNID}]
telemetryd.bn.active_streams[{#BNID}]
telemetryd.bn.age[{#BNID}]
telemetryd.bn.reported_connections[{#BNID}]
telemetryd.bn.fresh_rns[{#BNID}]
telemetryd.bn.connection_delta[{#BNID}]
```

## Trigger strategy

Do not trigger a definitive RN outage solely on `available=0`. That value is also zero for uncertainty.

Recommended separate events:

### Confirmed/reported RN offline

```text
known_offline = 1
```

Severity can be high because the upstream feed explicitly reported or deleted the RN connection.

### RN telemetry stale while BN healthy

```text
status_code = 2
```

This is a visibility or likely RN/link problem, but not proof of a physical outage.

### RN visibility lost with parent BN

```text
status_code = 3
```

Correlate or suppress this beneath the parent BN `status_code=0`/stale event to avoid an alert storm.

### Parent conflict

```text
status_code = 4
```

This indicates association or identity inconsistency and deserves a separate operational workflow.

### Timestamp defect

Alert or trend on:

```text
timestamp_quality != "source"
source_regressions increasing
```

A missing timestamp is a data-quality condition, not a device availability condition.

### BN/RN count inconsistency

Monitor `connection_count_delta`. A persistent nonzero delta means the BN-reported active count and the reconciler's fresh child set disagree. It is evidence of inconsistency, not enough by itself to name a specific RN down.

## Master/dependent item option

At very large scale, thousands of individual HTTP requests may be undesirable. Zabbix can instead fetch `/v1/snapshot?metrics=false` as a master item and use dependent-item preprocessing. Use `/v1/overview` when only collector counts and ingestion counters are needed; it does not walk every device. The dedicated scalar endpoints are simpler and isolate path ambiguity, but the snapshot option reduces request volume.

A future version can expose a Zabbix batch endpoint if direct HTTP item volume becomes the limiting factor.
