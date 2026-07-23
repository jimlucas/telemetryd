# Capture Console

`telemetryd` embeds an operator dashboard in the same binary as the gRPC receiver and HTTP API. It requires no Node.js build, CDN, external fonts, or separate web server.

## URL

With the default HTTP binding:

```text
http://127.0.0.1:8080/ui/
```

For access from another workstation, bind the HTTP listener to a routable address and open the collector host in a browser:

```bash
export TELEMETRYD_TOKEN='replace-with-a-long-random-value'

./bin/telemetryd \
  --grpc-listen=:50051 \
  --http-listen=0.0.0.0:8080 \
  --http-token="$TELEMETRYD_TOKEN"
```

```text
http://<collector-ip>:8080/ui/
```

The service writes the local dashboard URL to its startup log. The root path, `/`, redirects to `/ui/`.

## Authentication

The static dashboard shell is public so a browser can load it, but every data API it calls remains protected when `--http-token` is configured. Enter the bearer token using **Connect** in the upper-right corner.

The token is:

- kept only in browser `sessionStorage`;
- sent as `Authorization: Bearer <token>`;
- cleared when that browser tab/session is closed or when **Disconnect** is selected;
- never embedded in the page URL, local storage, or server-rendered HTML.

Binding the HTTP listener beyond loopback should always be paired with a bearer token, host firewall rules, and a TLS-terminating reverse proxy or private management network.

## What the console shows

### Capture status

The top row answers whether telemetry is reaching the process now:

- **Capture live / no active stream** based on current gRPC stream sessions;
- active and historical stream counts;
- recent message rate;
- known BN and RN inventory;
- missing, invalid, future, and regressed source-timestamp evidence;
- exact/repeated/sender-reported duplicate evidence.

A stream closing is shown as an upstream visibility event. It does not erase cached values.

### RN availability

The RN status chart and attention list use the reconciler rather than raw omission:

- `online_observed` — recent RN evidence with a healthy parent BN;
- `offline_reported` — an explicit disconnect/down state or authoritative connection-root delete;
- `stale_unconfirmed` — the BN is healthy, but the RN has not refreshed within its configured threshold;
- `unknown_upstream` — the parent BN stream is disconnected, stale, or unknown;
- `conflict` — multiple healthy BNs recently claimed the RN;
- `unknown` — no stronger conclusion is possible.

Fresh positive “connected” evidence does not hide a later parent-stream loss. When a BN stream closes, its children become `unknown_upstream` while their last-known values remain queryable.

### Capture integrity

The integrity panel calls attention to conditions that can make monitoring data misleading:

- observations with no source timestamp;
- timestamps classified as invalid, future, or regressed;
- repeated or exact duplicate values;
- sender-reported coalesced duplicates;
- decode/protocol errors;
- rejected devices or evicted paths caused by configured memory limits.

Receive time and source time are displayed separately. Missing source time is never silently replaced by collector receipt time.

### Recent current records

The records table is newest-first and searchable by device, hostname, parent BN, path, value, stream, and subscription scope. Filters include device kind, status, timestamp quality, path text, and age window.

This is intentionally **not an event-history table**. It is a view of the in-memory SNMP-style current-value map:

```text
one retained record per device + canonical path
```

A new submission for that key overwrites the old value. The retained record still carries cumulative sample, exact-duplicate, repeated-value, value-change, regression, and sender-reported duplicate counters. Clicking a row opens all retained metadata and decoded value information.

### Stream sessions

The stream table shows:

- stream ID and resolved BN identity;
- peer and logical subscription metadata;
- connection, first/last message, and disconnection times;
- active/closed state and close reason;
- message, update, delete, sync, decode-error, and protocol-error counters.

This is the first place to look when many RNs change to `unknown_upstream` together.

## APIs backing the console

```text
GET /v1/overview
GET /v1/summary
GET /v1/recent
GET /v1/attention/rns
GET /v1/streams
GET /v1/schema
```

Useful current-record examples:

```bash
# The 200 newest retained values.
curl -H "Authorization: Bearer $TELEMETRYD_TOKEN" \
  'http://127.0.0.1:8080/v1/recent?limit=200'

# RN signal records received during the last ten minutes.
curl -H "Authorization: Bearer $TELEMETRYD_TOKEN" \
  'http://127.0.0.1:8080/v1/recent?kind=rn&since=10m&q=signal&limit=200'

# Records whose source timestamp was absent.
curl -H "Authorization: Bearer $TELEMETRYD_TOKEN" \
  'http://127.0.0.1:8080/v1/recent?quality=missing&limit=200'

# The highest-priority RN conditions without allocating a full fleet snapshot.
curl -H "Authorization: Bearer $TELEMETRYD_TOKEN" \
  'http://127.0.0.1:8080/v1/attention/rns?limit=40'
```

`/v1/recent` accepts:

| Parameter | Meaning |
|---|---|
| `limit` | 1–1000 records; defaults to the server’s operator-view limit. |
| `kind` | `bn` or `rn`. |
| `id` | Exact device ID. |
| `bn` | Parent/source BN ID. |
| `path` | Case-insensitive path substring. |
| `q` | Case-insensitive search across identity, path, value, stream, and subscription fields. |
| `quality` | `source`, `missing`, `invalid_past`, `future`, or `regressed`. |
| `status` | Reconciled device status. |
| `since` | Go duration such as `10m`, or an RFC3339 timestamp. |
| `since_seconds` | Alternative age window in seconds. |

The response distinguishes `scanned`, `matched`, `returned`, and `truncated` so an operator can tell whether a filter or limit hid records.

## Device-list pagination

Large installations should query device lists with bounded pages:

```text
GET /v1/bns?q=site-a&limit=250&offset=0
GET /v1/rns?bn=BN-001&status=unknown_upstream&q=roof&limit=250&offset=0
```

Each response includes `total`, `count`, `offset`, `limit`, `truncated`, and `items`. Omitting `limit` preserves the original all-items API behavior for compatibility, but the dashboard avoids that path for routine polling.

## Troubleshooting

### The page loads but shows “authorization required”

Select **Connect** and enter the same value supplied through `--http-token` or `TELEMETRYD_TOKEN`. Verify it directly with:

```bash
curl -i -H "Authorization: Bearer $TELEMETRYD_TOKEN" \
  http://127.0.0.1:8080/v1/summary
```

### There are no active streams

Check the gRPC listening socket, the configured Tarana telemetry destination, firewall/NAT state, and `/v1/streams` for recent close reasons. Cached inventory and values may still appear; they are last-known state, not proof that capture is live.

### Values are present but source time says “missing”

That is preserved sender behavior. Use `received_at`, `stream_id`, `message_sequence`, and `observation_order` for auditable collector ordering, but do not interpret receive time as the device’s sample time.

### Several RNs become unknown together

Inspect their `parent_bn_id`, the BN status, and the stream-session table. A parent stream loss intentionally maps child RNs to `unknown_upstream`, not to confirmed offline.
