// internal/collector/collector.go
package collector

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"zabbix_agent_exporter/internal/config"
)

// zabbixQuerier is the subset of zabbixclient.Client used by the collector,
// defined locally so tests can substitute fakes without importing net.
type zabbixQuerier interface {
	GetItem(ctx context.Context, addr, key string) (string, error)
	Discover(ctx context.Context, addr, discoveryKey string) ([]map[string]string, error)
}

// ZabbixCollector implements prometheus.Collector for a single resolved
// target: it queries every item in the given modules sequentially against
// one Zabbix agent address.
type ZabbixCollector struct {
	client  zabbixQuerier
	cfg     *config.Config
	modules []string
	addr    string
	logger  *zap.Logger
}

// New builds a collector for one scrape of addr using the given modules,
// looked up by name in cfg.Modules.
func New(client zabbixQuerier, cfg *config.Config, modules []string, addr string, logger *zap.Logger) *ZabbixCollector {
	return &ZabbixCollector{client: client, cfg: cfg, modules: modules, addr: addr, logger: logger}
}

var (
	scrapeSuccessDesc  = prometheus.NewDesc("zabbix_scrape_success", "scrape_success", nil, nil)
	scrapeDurationDesc = prometheus.NewDesc("zabbix_scrape_duration_seconds", "scrape_duration_seconds", nil, nil)
	itemErrorsDesc     = prometheus.NewDesc("zabbix_item_errors_total", "item_errors_total", []string{"item"}, nil)
)

// Describe implements prometheus.Collector. Descriptors are dynamic per
// module configuration, so this intentionally sends nothing, marking the
// collector as unchecked (standard practice for dynamically-shaped exporters
// like blackbox_exporter and snmp_exporter).
func (c *ZabbixCollector) Describe(ch chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *ZabbixCollector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx := context.Background()

	success := 1.0
	firstContact := true
	itemErrors := map[string]int{}

	for _, moduleName := range c.modules {
		module, ok := c.cfg.Modules[moduleName]
		if !ok {
			c.logger.Warn("unknown module referenced", zap.String("module", moduleName))
			continue
		}

		for _, item := range module.Items {
			value, err := c.client.GetItem(ctx, c.addr, item.Key)
			if err != nil {
				c.logger.Warn("item query failed", zap.String("item", item.Key), zap.Error(err))
				itemErrors[item.Key]++
				if firstContact {
					success = 0
				}
				continue
			}
			firstContact = false
			c.emitValue(ch, item.Metric, item.Help, item.Type, value, nil, nil)
		}

		for _, di := range module.DiscoveredItems {
			entities, err := c.client.Discover(ctx, c.addr, di.DiscoveryKey)
			if err != nil {
				c.logger.Warn("discovery query failed", zap.String("item", di.DiscoveryKey), zap.Error(err))
				itemErrors[di.DiscoveryKey]++
				if firstContact {
					success = 0
				}
				continue
			}
			firstContact = false

			for _, entity := range entities {
				labelValue := entity[di.LabelField]
				valueKey := substituteMacro(di.ValueKey, di.LabelField, labelValue)
				value, err := c.client.GetItem(ctx, c.addr, valueKey)
				if err != nil {
					c.logger.Warn("discovered item query failed", zap.String("item", valueKey), zap.Error(err))
					itemErrors[valueKey]++
					continue
				}
				c.emitValue(ch, di.Metric, di.Help, di.Type, value, []string{di.LabelName}, []string{labelValue})
			}
		}
	}

	ch <- prometheus.MustNewConstMetric(scrapeSuccessDesc, prometheus.GaugeValue, success)
	ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, time.Since(start).Seconds())
	for item, count := range itemErrors {
		ch <- prometheus.MustNewConstMetric(itemErrorsDesc, prometheus.CounterValue, float64(count), item)
	}
}

func (c *ZabbixCollector) emitValue(ch chan<- prometheus.Metric, metricName, help string, metricType config.MetricType, rawValue string, labelNames, labelValues []string) {
	value, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		c.logger.Warn("non-numeric item value skipped", zap.String("metric", metricName), zap.String("raw_value", rawValue))
		return
	}

	valueType := prometheus.GaugeValue
	if metricType == config.MetricTypeCounter {
		valueType = prometheus.CounterValue
	}

	desc := prometheus.NewDesc(metricName, help, labelNames, nil)
	ch <- prometheus.MustNewConstMetric(desc, valueType, value, labelValues...)
}

func substituteMacro(template, macro, value string) string {
	result := ""
	for i := 0; i < len(template); {
		if i+len(macro) <= len(template) && template[i:i+len(macro)] == macro {
			result += value
			i += len(macro)
			continue
		}
		result += string(template[i])
		i++
	}
	return result
}
