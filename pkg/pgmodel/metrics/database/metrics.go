package database

import (
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/timescale/promscale/pkg/util"
)

var (
	dbHealthErrors = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: util.PromNamespace,
			Subsystem: "sql_database",
			Name:      "health_check_errors_total",
			Help:      "Total number of database health check errors.",
		},
	)
	upMetric = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name:        "up",
			Help:        "Up represents if the database metrics engine is running or not.",
			ConstLabels: map[string]string{"type": "promscale_sql"},
		},
	)
	dbNetworkLatency = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace:   util.PromNamespace,
			Subsystem:   "sql_database",
			Name:        "network_latency_milliseconds",
			Help:        "Network latency between Promscale and Database. A negative value indicates a failed health check.",
			ConstLabels: map[string]string{"type": "promscale_sql"},
		},
	)
)

func init() {
	prometheus.MustRegister(dbHealthErrors, upMetric, dbNetworkLatency)
}

type metricQueryWrap struct {
	// Multiple metrics could be retrieved via single query
	// In that case they should appear in the same order as
	// corresponding the columns in the query's result.
	metrics       []prometheus.Collector
	query         string
	isHealthCheck bool // if set only metrics[0] is used
}

func gauges(opts ...prometheus.GaugeOpts) []prometheus.Collector {
	res := make([]prometheus.Collector, 0, len(opts))
	for _, opt := range opts {
		res = append(res, prometheus.NewGauge(opt))
	}
	return res
}
func counters(opts ...prometheus.CounterOpts) []prometheus.Collector {
	res := make([]prometheus.Collector, 0, len(opts))
	for _, opt := range opts {
		res = append(res, prometheus.NewCounter(opt))
	}
	return res
}

var metrics = []metricQueryWrap{
	{
		metrics: counters(
			prometheus.CounterOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "health_check_total",
				Help:      "Total number of database health checks performed.",
			},
		),
		query:         "SELECT 1",
		isHealthCheck: true,
	},
	{
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "chunks_count",
				Help:      "Total number of chunks in TimescaleDB currently.",
			},
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "chunks_compressed_count",
				Help:      "Total number of compressed chunks in TimescaleDB currently.",
			},
		),
		// Compressed_chunk_id is null for both yet to be compressed and already compressed chunks.
		query: `SELECT 
				count(*) FILTER (WHERE dropped=false AND compressed_chunk_id IS NULL)::BIGINT AS chunks_count,
				count(*) FILTER (WHERE dropped=false AND compressed_chunk_id IS NOT NULL)::BIGINT AS chunks_compressed_count
			FROM _timescaledb_catalog.chunk`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "chunks_metrics_expired_count",
				Help:      "The number of metrics chunks soon to be removed by maintenance jobs.",
			},
		),
		query: `WITH conf AS MATERIALIZED (SELECT _prom_catalog.get_default_retention_period() AS def_retention)
		SELECT count(*)::BIGINT
		FROM _timescaledb_catalog.dimension_slice ds
			 INNER JOIN _timescaledb_catalog.dimension d ON (d.id = ds.dimension_id)
			 INNER JOIN _timescaledb_catalog.hypertable h ON (h.id = d.hypertable_id)
			 INNER JOIN _prom_catalog.metric m ON (m.table_name = h.table_name AND m.table_schema = h.schema_name)
			 JOIN conf ON TRUE
		WHERE ds.range_start < _timescaledb_internal.time_to_internal(now() - coalesce(m.retention_period, conf.def_retention))
		  AND ds.range_end < _timescaledb_internal.time_to_internal(now() - coalesce(m.retention_period, conf.def_retention))`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "chunks_metrics_uncompressed_count",
				Help:      "The number of metrics chunks soon to be compressed by maintenance jobs.",
			},
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "chunks_metrics_delayed_compression_count",
				Help:      "The number of metrics chunks not-compressed due to a set delay.",
			},
		),
		query: `WITH chunk_candidates AS MATERIALIZED (
				SELECT chcons.dimension_slice_id, h.table_name, h.schema_name
				FROM _timescaledb_catalog.chunk_constraint chcons
					INNER JOIN _timescaledb_catalog.chunk c ON c.id = chcons.chunk_id
					INNER JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
				WHERE c.dropped IS FALSE
				AND h.compression_state = 1 -- compression_enabled = TRUE
				AND (c.status & 1) != 1 -- only check for uncompressed chunks
			) 
			SELECT 
				count(*) FILTER(WHERE m.delay_compression_until IS NULL OR m.delay_compression_until < now())::BIGINT AS uncompressed,
				count(*) FILTER(WHERE m.delay_compression_until IS NOT NULL AND m.delay_compression_until >= now())::BIGINT AS delayed_compression
			FROM chunk_candidates cc
				INNER JOIN _timescaledb_catalog.dimension_slice ds ON ds.id = cc.dimension_slice_id
				INNER JOIN _prom_catalog.metric m ON (m.table_name = cc.table_name AND m.table_schema = cc.schema_name)
			WHERE NOT m.is_view
			AND ds.range_start <= _timescaledb_internal.time_to_internal(now() - interval '1 hour')
			AND ds.range_end <= _timescaledb_internal.time_to_internal(now() - interval '1 hour')`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "chunks_traces_expired_count",
				Help:      "The number of traces chunks soon to be removed by maintenance jobs.",
			},
		),
		query: `WITH conf AS MATERIALIZED (SELECT coalesce(ps_trace.get_trace_retention_period(), interval '0 day') AS def_retention)
		SELECT count(*)::BIGINT
		FROM _timescaledb_catalog.dimension_slice ds
			 INNER JOIN _timescaledb_catalog.dimension d ON (d.id = ds.dimension_id)
			 INNER JOIN _timescaledb_catalog.hypertable h ON (h.id = d.hypertable_id)
			 JOIN conf ON TRUE
		WHERE ds.range_start < _timescaledb_internal.time_to_internal(now() - conf.def_retention)
		  AND ds.range_end < _timescaledb_internal.time_to_internal(now() - conf.def_retention)
		  AND h.schema_name = '_ps_trace'`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "chunks_traces_uncompressed_count",
				Help:      "The number of traces chunks soon to be compressed by maintenance jobs.",
			},
		),
		query: `WITH chunk_candidates AS MATERIALIZED (
				SELECT chcons.dimension_slice_id
				FROM _timescaledb_catalog.chunk_constraint chcons
					INNER JOIN _timescaledb_catalog.chunk c ON c.id = chcons.chunk_id
					INNER JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
				WHERE c.dropped IS FALSE
				AND h.schema_name = '_ps_trace'
				AND h.compression_state = 1 -- compression_enabled = TRUE
				AND (c.status & 1) != 1 -- only check for uncompressed chunks
			)
			SELECT count(*)::BIGINT
			FROM chunk_candidates cc
				INNER JOIN _timescaledb_catalog.dimension_slice ds ON ds.id = cc.dimension_slice_id
			WHERE ds.range_start <= _timescaledb_internal.time_to_internal(now() - interval '1 hour')
			AND ds.range_end <= _timescaledb_internal.time_to_internal(now() - interval '1 hour')`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "compression_status",
				Help:      "Compression status in TimescaleDB.",
			},
		),
		query: `select (case when (value = 'true') then 1 else 0 end) from _prom_catalog.get_default_value('metric_compression') value`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "worker_count",
				Help:      "Number of TimescaleDB background workers.",
			},
		),
		query: `select current_setting('timescaledb.max_background_workers')::BIGINT`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "worker_maintenance_job",
				Help:      "Number of Promscale maintenance workers.",
			},
		),
		query: `select count(*) from timescaledb_information.jobs where proc_name = 'execute_maintenance_job'`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "worker_maintenance_job_failed",
				Help:      "Number of Promscale maintenance jobs that failed.",
			},
		),
		query: `select count(stats.last_run_status)
			from timescaledb_information.job_stats stats
			inner join
			timescaledb_information.jobs jobs
				on jobs.job_id = stats.job_id
			where jobs.proc_name = 'execute_maintenance_job' and stats.last_run_status = 'Failed'`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "worker_maintenance_job_start_timestamp_seconds",
				Help:      "Timestamp in unix seconds for last successful execution of Promscale maintenance job.",
			},
		),
		query: `SELECT extract(
			epoch FROM (SELECT COALESCE(
				(SELECT last_run_started_at AS job_running_since
					FROM   timescaledb_information.job_stats WHERE  last_run_started_at > last_successful_finish
						AND last_run_status = 'Success'
				),
				CURRENT_TIMESTAMP
			)))::BIGINT`,
	}, {
		metrics: gauges(
			prometheus.GaugeOpts{
				Namespace: util.PromNamespace,
				Subsystem: "sql_database",
				Name:      "metric_count",
				Help:      "Total number of metrics in the database.",
			},
		),
		query: `select count(*)::bigint from _prom_catalog.metric`,
	},
}

// GetMetric returns the first metric whose Name matches the supplied name.
func GetMetric(name string) (prometheus.Metric, error) {
	for _, ms := range metrics {
		for _, m := range ms.metrics {
			metric := getMetric(m)
			str, err := util.ExtractMetricDesc(metric)
			if err != nil {
				return nil, fmt.Errorf("extract metric string")
			}
			if strings.Contains(str, name) {
				return metric, nil
			}
		}
	}
	return nil, nil
}

func getMetric(c prometheus.Collector) prometheus.Metric {
	switch n := c.(type) {
	case prometheus.Gauge:
		return n
	case prometheus.Counter:
		return n
	default:
		panic(fmt.Sprintf("invalid type: %T", n))
	}
}
