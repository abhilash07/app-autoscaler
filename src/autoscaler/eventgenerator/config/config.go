package config

import (
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/locket"

	"gopkg.in/yaml.v2"

	"autoscaler/models"
)

const (
	DefaultLoggingLevel              string        = "info"
	DefaultPolicyPollerInterval      time.Duration = 40 * time.Second
	DefaultAggregatorExecuteInterval time.Duration = 40 * time.Second
	DefaultMetricPollerCount         int           = 20
	DefaultAppMonitorChannelSize     int           = 200
	DefaultEvaluationExecuteInterval time.Duration = 40 * time.Second
	DefaultEvaluatorCount            int           = 20
	DefaultTriggerArrayChannelSize   int           = 200
	DefaultLockTTL                   time.Duration = locket.DefaultSessionTTL
	DefaultRetryInterval             time.Duration = locket.RetryInterval
)

type LoggingConfig struct {
	Level string `yaml:"level"`
}

type DBConfig struct {
	PolicyDBUrl    string `yaml:"policy_db_url"`
	AppMetricDBUrl string `yaml:"app_metrics_db_url"`
}

type AggregatorConfig struct {
	MetricPollerCount         int           `yaml:"metric_poller_count"`
	AppMonitorChannelSize     int           `yaml:"app_monitor_channel_size"`
	AggregatorExecuteInterval time.Duration `yaml:"aggregator_execute_interval"`
	PolicyPollerInterval      time.Duration `yaml:"policy_poller_interval"`
}

type EvaluatorConfig struct {
	EvaluatorCount            int           `yaml:"evaluator_count"`
	TriggerArrayChannelSize   int           `yaml:"trigger_array_channel_size"`
	EvaluationManagerInterval time.Duration `yaml:"evaluation_manager_execute_interval"`
}

type ScalingEngineConfig struct {
	ScalingEngineUrl string          `yaml:"scaling_engine_url"`
	TLSClientCerts   models.TLSCerts `yaml:"tls"`
}

type MetricCollectorConfig struct {
	MetricCollectorUrl string          `yaml:"metric_collector_url"`
	TLSClientCerts     models.TLSCerts `yaml:"tls"`
}

type LockConfig struct {
	LockTTL             time.Duration `yaml:"lock_ttl"`
	LockRetryInterval   time.Duration `yaml:"lock_retry_interval"`
	ConsulClusterConfig string        `yaml:"consul_cluster_config"`
}

type Config struct {
	Logging         LoggingConfig         `yaml:"logging"`
	DB              DBConfig              `yaml:"db"`
	Aggregator      AggregatorConfig      `yaml:"aggregator"`
	Evaluator       EvaluatorConfig       `yaml:"evaluator"`
	ScalingEngine   ScalingEngineConfig   `yaml:"scalingEngine"`
	MetricCollector MetricCollectorConfig `yaml:"metricCollector"`
	Lock            LockConfig            `yaml:"lock"`
}

func LoadConfig(bytes []byte) (*Config, error) {
	conf := &Config{
		Logging: LoggingConfig{
			Level: DefaultLoggingLevel,
		},
		Aggregator: AggregatorConfig{
			AggregatorExecuteInterval: DefaultAggregatorExecuteInterval,
			PolicyPollerInterval:      DefaultPolicyPollerInterval,
			MetricPollerCount:         DefaultMetricPollerCount,
			AppMonitorChannelSize:     DefaultAppMonitorChannelSize,
		},
		Evaluator: EvaluatorConfig{
			EvaluationManagerInterval: DefaultEvaluationExecuteInterval,
			EvaluatorCount:            DefaultEvaluatorCount,
			TriggerArrayChannelSize:   DefaultTriggerArrayChannelSize,
		},
		Lock: LockConfig{
			LockRetryInterval: DefaultRetryInterval,
			LockTTL:           DefaultLockTTL,
		},
	}
	err := yaml.Unmarshal(bytes, &conf)
	if err != nil {
		return nil, err
	}
	conf.Logging.Level = strings.ToLower(conf.Logging.Level)
	return conf, nil
}

func (c *Config) Validate() error {
	if c.DB.PolicyDBUrl == "" {
		return fmt.Errorf("Configuration error: Policy DB url is empty")
	}
	if c.DB.AppMetricDBUrl == "" {
		return fmt.Errorf("Configuration error: AppMetric DB url is empty")
	}
	if c.ScalingEngine.ScalingEngineUrl == "" {
		return fmt.Errorf("Configuration error: Scaling engine url is empty")
	}
	if c.MetricCollector.MetricCollectorUrl == "" {
		return fmt.Errorf("Configuration error: Metric collector url is empty")
	}
	if c.Lock.ConsulClusterConfig == "" {
		return fmt.Errorf("Configuration error: Consul Cluster Config is empty")
	}
	if c.Aggregator.AggregatorExecuteInterval <= time.Duration(0) {
		return fmt.Errorf("Configuration error: aggregator execute interval is less-equal than 0")
	}
	if c.Aggregator.PolicyPollerInterval <= time.Duration(0) {
		return fmt.Errorf("Configuration error: policy poller interval is less-equal than 0")
	}
	if c.Aggregator.MetricPollerCount <= 0 {
		return fmt.Errorf("Configuration error: metric poller count is less-equal than 0")
	}
	if c.Aggregator.AppMonitorChannelSize <= 0 {
		return fmt.Errorf("Configuration error: appMonitor channel size is less-equal than 0")
	}
	if c.Evaluator.EvaluationManagerInterval <= time.Duration(0) {
		return fmt.Errorf("Configuration error: evalution manager execeute interval is less-equal than 0")
	}
	if c.Evaluator.EvaluatorCount <= 0 {
		return fmt.Errorf("Configuration error: evaluator count is less-equal than 0")
	}
	if c.Evaluator.TriggerArrayChannelSize <= 0 {
		return fmt.Errorf("Configuration error: trigger-array channel size is less-equal than 0")
	}
	if c.Lock.LockRetryInterval <= 0 {
		return fmt.Errorf("Configuration error: lock retry interval is less than or equal to 0")
	}
	if c.Lock.LockTTL <= 0 {
		return fmt.Errorf("Configuration error: lock ttl is less than or equal to 0")
	}
	return nil

}
