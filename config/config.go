package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 应用总配置
type Config struct {
	SIS      SISConfig      `yaml:"sis"`
	MQTT     MQTTConfig     `yaml:"mqtt"`
	Collect  CollectConfig  `yaml:"collect"`
	PointCSV string         `yaml:"point_csv"`
	Health   HealthConfig   `yaml:"health"`
	Log      LogConfig      `yaml:"log"`
}

// SISConfig SIS 系统 HTTP 接口配置
type SISConfig struct {
	BaseURL        string `yaml:"base_url"`         // http://host:port
	DataPointPath  string `yaml:"data_point_path"`  // /api/Values/GetDataPoint
	TimeoutSeconds int    `yaml:"timeout_seconds"`  // HTTP 超时（秒）
	BatchSize      int    `yaml:"batch_size"`       // 每批请求测点数
}

// MQTTConfig MQTT 连接配置
type MQTTConfig struct {
	Broker       string `yaml:"broker"`        // tcp://host:1883
	ClientID     string `yaml:"client_id"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	KeepAlive    int    `yaml:"keep_alive"`    // 心跳间隔（秒）
	CleanSession bool   `yaml:"clean_session"`
	QoS          byte   `yaml:"qos"`           // 消息质量等级，建议 1

	// TLS
	TLSEnable   bool   `yaml:"tls_enable"`
	TLSCACert   string `yaml:"tls_ca_cert"`
	TLSClientCert string `yaml:"tls_client_cert"`
	TLSClientKey  string `yaml:"tls_client_key"`

	// 重连策略（指数退避）
	ReconnectMinSeconds int `yaml:"reconnect_min_seconds"` // 最小重连等待（秒），默认 1
	ReconnectMaxSeconds int `yaml:"reconnect_max_seconds"` // 最大重连等待（秒），默认 120
}

// TopicConfig MQTT 主题配置
type TopicConfig struct {
	StatusTopic string `yaml:"status_topic"` // device/{device_id}/status
	CmdTopic    string `yaml:"cmd_topic"`    // device/{device_id}/cmd
	DataTopic   string `yaml:"data_topic"`   // device/{device_id}/data
	DeviceID    string `yaml:"device_id"`    // 设备唯一编码
}

// CollectConfig 采集任务配置
type CollectConfig struct {
	Topic            TopicConfig `yaml:"topic"`
	CollectIntervalS int         `yaml:"collect_interval_s"`  // 采集间隔（秒），默认 10
	StatusIntervalS  int         `yaml:"status_interval_s"`   // 状态上报间隔（秒），默认 30
	MQTTBatchSize    int         `yaml:"mqtt_batch_size"`     // 每条 MQTT 消息携带的测点数量
}

// HealthConfig HTTP 健康检查配置
type HealthConfig struct {
	Port int `yaml:"port"` // 默认 8080
}

// LogConfig 日志配置
type LogConfig struct {
	Level    string `yaml:"level"`     // debug / info / warn / error
	FilePath string `yaml:"file_path"` // 留空则仅输出控制台
}

// Load 从文件加载配置，并应用环境变量覆盖
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 展开环境变量
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err = yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	cfg.applyDefaults()

	if err = cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.SIS.DataPointPath == "" {
		c.SIS.DataPointPath = "/api/Values/GetDataPoint"
	}
	if c.SIS.TimeoutSeconds <= 0 {
		c.SIS.TimeoutSeconds = 10
	}
	if c.SIS.BatchSize <= 0 {
		c.SIS.BatchSize = 50
	}
	if c.MQTT.KeepAlive <= 0 {
		c.MQTT.KeepAlive = 60
	}
	if c.MQTT.ReconnectMinSeconds <= 0 {
		c.MQTT.ReconnectMinSeconds = 1
	}
	if c.MQTT.ReconnectMaxSeconds <= 0 {
		c.MQTT.ReconnectMaxSeconds = 120
	}
	if c.Collect.CollectIntervalS <= 0 {
		c.Collect.CollectIntervalS = 10
	}
	if c.Collect.StatusIntervalS <= 0 {
		c.Collect.StatusIntervalS = 30
	}
	if c.Collect.MQTTBatchSize <= 0 {
		c.Collect.MQTTBatchSize = 100
	}
	if c.Health.Port <= 0 {
		c.Health.Port = 8080
	}
	if c.PointCSV == "" {
		c.PointCSV = "points.csv"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}

	// 若主题未配置，使用 device_id 自动生成
	deviceID := c.Collect.Topic.DeviceID
	if deviceID != "" {
		if c.Collect.Topic.StatusTopic == "" {
			c.Collect.Topic.StatusTopic = fmt.Sprintf("device/%s/status", deviceID)
		}
		if c.Collect.Topic.CmdTopic == "" {
			c.Collect.Topic.CmdTopic = fmt.Sprintf("device/%s/cmd", deviceID)
		}
		if c.Collect.Topic.DataTopic == "" {
			c.Collect.Topic.DataTopic = fmt.Sprintf("device/%s/data", deviceID)
		}
	}
}

func (c *Config) validate() error {
	if c.SIS.BaseURL == "" {
		return fmt.Errorf("配置验证失败: sis.base_url 不能为空")
	}
	if c.MQTT.Broker == "" {
		return fmt.Errorf("配置验证失败: mqtt.broker 不能为空")
	}
	if c.Collect.Topic.DeviceID == "" {
		return fmt.Errorf("配置验证失败: collect.topic.device_id 不能为空")
	}
	if c.Collect.Topic.StatusTopic == "" {
		return fmt.Errorf("配置验证失败: collect.topic.status_topic 不能为空")
	}
	if c.Collect.Topic.CmdTopic == "" {
		return fmt.Errorf("配置验证失败: collect.topic.cmd_topic 不能为空")
	}
	if c.Collect.Topic.DataTopic == "" {
		return fmt.Errorf("配置验证失败: collect.topic.data_topic 不能为空")
	}
	return nil
}
