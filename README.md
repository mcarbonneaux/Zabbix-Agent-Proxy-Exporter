# zabbix_agent_exporter

Prometheus exporter that proxies scrapes to legacy Zabbix passive agents.
It lets a fleet of old Zabbix agents (on hosts where `node_exporter` isn't
available) be monitored by Prometheus without running a Zabbix server.

## Build

    go build -o bin/zabbix_agent_exporter ./cmd/zabbix_agent_exporter

## Run

    ./bin/zabbix_agent_exporter -config configs/config.example.yaml

## Configuration

See `configs/config.example.yaml` for a full example. Key concepts:

- `defaults`: fallback Zabbix agent port/timeout.
- `hosts`: per-host port/timeout overrides.
- `modules`: named sets of Zabbix item -> Prometheus metric mappings,
  including LLD-discovered items (filesystems, network interfaces, ...).
- `groups`: lists of hosts associated with one or more modules; a host's
  resolved modules are the union of every group that contains it.

The config file is watched for changes and hot-reloaded without restarting
the process.

## Prometheus scrape config

    scrape_configs:
      - job_name: zabbix
        static_configs:
          - targets: ["srv-old-01", "srv-old-02"]
        relabel_configs:
          - source_labels: [__address__]
            target_label: __param_target
          - target_label: __address__
            replacement: localhost:9290

## Endpoints

- `GET /metrics?target=<host>[&module=<name>]*[&port=<p>][&timeout=<d>]`
- `GET /healthz`
