# Tarana path mapping

The adapter retains every gNMI path generically. The paths below receive additional identity or summary semantics.

## BN

| Base path | Semantic use |
|---|---|
| `/system/state/hostname` | BN hostname. |
| `/connections/global/state/active-connections` | BN-reported active RN count and consistency delta. |
| `/radios/radio/state/rx-signal-level/avg` | Generic per-radio latest metric. |
| `/platform/components/component/state/mac-address` | BN MAC hint; the last submitted value is retained per canonical component path. |
| paths ending `serial-number` or `serial-no` | Retained as generic latest values; they do not silently rename an established BN identity. |

## RN

The RN ID is taken from a key on the `connections/connection` element. Accepted normalized key names include:

```text
connection_device-id
device-id
rn-id
serial-number
id
name
```

More specific key names are tried before generic `id`/`name`.

| Base path | Semantic use |
|---|---|
| `/connections/connection/system/state/hostname` | RN hostname. |
| `/connections/connection/platform/state/mac-address` | RN MAC address. |
| `/connections/connection/state/dl-snr` | Generic metric; common Zabbix KPI. |
| `/connections/connection/state/ul-snr` | Generic metric; common Zabbix KPI. |
| `/connections/connection/state/path-loss` | Generic metric; common Zabbix KPI. |
| `/connections/connection/state/rf-range` | Generic metric; common Zabbix KPI. |
| `/connections/connection/radios/radio/state/rx-signal-level/avg` | Generic per-radio latest metric. |

Only direct operational-state leaves under the keyed `/connections/connection/state` container are normalized when their leaf name resembles:

```text
connected
connection-state
oper-status
operational-status
status
link-state
association-state
```

Recognized online values include `up`, `online`, `connected`, `active`, `enabled`, `associated`, `registered`, boolean true, and numeric `1`. Offline equivalents include boolean false and numeric `0`. Other numeric values and nested component/radio status leaves are deliberately not treated as whole-RN availability evidence.

Because Tarana versions can add or rename paths, inspect `/v1/rns/{id}?metrics=true` and `/v1/bns/{id}?metrics=true` before making a path a hard monitoring dependency.
