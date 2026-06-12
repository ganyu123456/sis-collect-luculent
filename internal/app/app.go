// Package app 核心应用逻辑：调度 SIS 采集、MQTT 发布/订阅
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/sis-collect-luculent/config"
	"github.com/sis-collect-luculent/internal/collector"
	mqttclient "github.com/sis-collect-luculent/internal/mqtt"
	"github.com/sis-collect-luculent/internal/pointtable"
)

// ===== MQTT 消息结构体 =====

// StatusMessage device/{id}/status 消息体
type StatusMessage struct {
	Timestamp          int64  `json:"timestamp"`
	RunState           string `json:"runState"`
	TaskControl        int    `json:"taskControl"`
	CollectPointTotal  int    `json:"collectPointTotal"`
	CollectPointOnline int    `json:"collectPointOnline"`
	LastCollectTime    int64  `json:"lastCollectTime"`
	CollectInterval    int    `json:"collectInterval"`
}

// PointData batchData 中单个测点的数据格式
type PointData struct {
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"` // SIS 测点时间戳（秒）
	State     int     `json:"state"`     // 1=正常，0=异常
}

// DataMessage device/{id}/data 消息体（单批）
type DataMessage struct {
	Timestamp int64                `json:"timestamp"`
	DeviceID  string               `json:"deviceId"`
	BatchData map[string]PointData `json:"batchData"`
}

// CmdMessage device/{id}/cmd 消息体（平台下发指令）
type CmdMessage struct {
	RequestID string                 `json:"requestId"`
	Params    map[string]interface{} `json:"params"`
	Timestamp int64                  `json:"timestamp"`
}

// ===== App =====

// App 数采应用主体
type App struct {
	cfg        *config.Config
	logger     *zap.Logger
	table      *pointtable.Table
	collector  *collector.Collector
	mqttClient *mqttclient.Client

	// 运行状态（原子操作保证线程安全）
	taskRunning     atomic.Int32  // 1=运行中 0=已停止
	collectInterval atomic.Int32  // 采集间隔（秒）

	// 上次采集时间和在线测点数
	mu              sync.RWMutex
	lastCollectTime time.Time
	onlineCount     int

	cancel context.CancelFunc
}

// New 创建 App 实例
func New(cfg *config.Config, logger *zap.Logger) (*App, error) {
	// 加载点表
	table, err := pointtable.LoadFromCSV(cfg.PointCSV)
	if err != nil {
		return nil, fmt.Errorf("加载点表失败: %w", err)
	}
	logger.Info("点表加载完成",
		zap.Int("total", table.Total()),
		zap.Int("enabled", table.EnabledTotal()),
		zap.String("csv", cfg.PointCSV),
	)

	// 创建采集器
	col := collector.New(&cfg.SIS, logger)

	// 创建 MQTT 客户端
	mqttCli, err := mqttclient.New(&cfg.MQTT, logger)
	if err != nil {
		return nil, fmt.Errorf("MQTT 客户端初始化失败: %w", err)
	}

	app := &App{
		cfg:        cfg,
		logger:     logger,
		table:      table,
		collector:  col,
		mqttClient: mqttCli,
	}
	app.taskRunning.Store(1)
	app.collectInterval.Store(int32(cfg.Collect.CollectIntervalS))

	return app, nil
}

// Run 启动采集循环（阻塞，直到 ctx 取消）
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	defer cancel()

	// 订阅指令主题
	if err := a.subscribeCmd(); err != nil {
		return fmt.Errorf("订阅指令主题失败: %w", err)
	}

	// 启动后 5s 内先推送一次状态
	go func() {
		time.Sleep(2 * time.Second)
		a.publishStatus()
	}()

	// 状态上报定时器（周期）
	statusTicker := time.NewTicker(time.Duration(a.cfg.Collect.StatusIntervalS) * time.Second)
	defer statusTicker.Stop()

	a.logger.Info("数采应用启动",
		zap.Int("collect_interval_s", int(a.collectInterval.Load())),
		zap.Int("status_interval_s", a.cfg.Collect.StatusIntervalS),
		zap.Int("mqtt_batch_size", a.cfg.Collect.MQTTBatchSize),
		zap.String("data_topic", a.cfg.Collect.Topic.DataTopic),
	)

	for {
		// 采集间隔可能被平台指令动态修改，每次循环重新读取
		interval := time.Duration(a.collectInterval.Load()) * time.Second
		collectTimer := time.NewTimer(interval)

		select {
		case <-runCtx.Done():
			collectTimer.Stop()
			a.logger.Info("数采应用已停止")
			return nil

		case <-collectTimer.C:
			if a.taskRunning.Load() == 1 {
				a.doCollect(runCtx)
			}

		case <-statusTicker.C:
			a.publishStatus()
		}
	}
}

// Stop 停止采集任务（保留 MQTT 连接，状态仍推送）
func (a *App) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	a.mqttClient.Disconnect()
}

// Status 返回当前运行状态（用于健康检查）
type Status struct {
	TaskRunning     bool      `json:"task_running"`
	CollectInterval int       `json:"collect_interval_s"`
	PointTotal      int       `json:"point_total"`
	PointEnabled    int       `json:"point_enabled"`
	OnlineCount     int       `json:"online_count"`
	LastCollectTime time.Time `json:"last_collect_time"`
	MQTTConnected   bool      `json:"mqtt_connected"`
}

// GetStatus 获取运行状态
func (a *App) GetStatus() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Status{
		TaskRunning:     a.taskRunning.Load() == 1,
		CollectInterval: int(a.collectInterval.Load()),
		PointTotal:      a.table.Total(),
		PointEnabled:    a.table.EnabledTotal(),
		OnlineCount:     a.onlineCount,
		LastCollectTime: a.lastCollectTime,
		MQTTConnected:   a.mqttClient.IsConnected(),
	}
}

// ===== 内部方法 =====

// doCollect 执行一次采集并将数据发布到 data 主题
func (a *App) doCollect(ctx context.Context) {
	pointNames := a.table.EnabledPointNames()
	if len(pointNames) == 0 {
		a.logger.Warn("没有启用的测点，跳过本次采集")
		return
	}

	result := a.collector.FetchBatch(ctx, pointNames)

	a.mu.Lock()
	a.lastCollectTime = result.CollectTime
	a.onlineCount = result.OnlineCount
	a.mu.Unlock()

	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			a.logger.Warn("采集批次错误", zap.Error(e))
		}
	}

	a.logger.Debug("采集完成",
		zap.Int("online", result.OnlineCount),
		zap.Int("total_requested", result.TotalRequested),
		zap.Duration("elapsed", time.Since(result.CollectTime)),
	)

	// 将结果按 mqtt_batch_size 分批发布
	a.publishData(result)

	// 状态变更（在线数可能变化）时立即上报
	a.publishStatus()
}

// publishData 将采集结果分批推送到 data 主题
func (a *App) publishData(result *collector.CollectResult) {
	if len(result.Points) == 0 {
		return
	}

	batchSize := a.cfg.Collect.MQTTBatchSize
	if batchSize <= 0 {
		batchSize = 100
	}

	deviceID := a.cfg.Collect.Topic.DeviceID
	topic := a.cfg.Collect.Topic.DataTopic
	ts := result.CollectTime.UnixMilli()

	// 将 map 转为切片后分批
	allPoints := make([]*collector.PointValue, 0, len(result.Points))
	for _, pv := range result.Points {
		allPoints = append(allPoints, pv)
	}

	batches := splitSlice(allPoints, batchSize)
	for batchIdx, batch := range batches {
		batchData := make(map[string]PointData, len(batch))
		for _, pv := range batch {
			batchData[pv.Key] = PointData{
				Value:     pv.Value.PointValue,
				Timestamp: pv.PointTimeSec(),
				State:     1,
			}
		}

		msg := DataMessage{
			Timestamp: ts,
			DeviceID:  deviceID,
			BatchData: batchData,
		}

		payload, err := json.Marshal(msg)
		if err != nil {
			a.logger.Error("序列化 data 消息失败", zap.Int("batch", batchIdx), zap.Error(err))
			continue
		}

		if err := a.mqttClient.Publish(topic, payload); err != nil {
			a.logger.Error("发布 data 消息失败",
				zap.Int("batch", batchIdx),
				zap.String("topic", topic),
				zap.Error(err),
			)
		} else {
			a.logger.Debug("data 消息已发布",
				zap.Int("batch", batchIdx+1),
				zap.Int("total_batches", len(batches)),
				zap.Int("points", len(batch)),
			)
		}
	}
}

// publishStatus 推送状态消息到 status 主题
func (a *App) publishStatus() {
	a.mu.RLock()
	online := a.onlineCount
	lastCollect := a.lastCollectTime
	a.mu.RUnlock()

	runState := "Stopped"
	if a.taskRunning.Load() == 1 {
		runState = "Running"
	}

	msg := StatusMessage{
		Timestamp:          time.Now().UnixMilli(),
		RunState:           runState,
		TaskControl:        int(a.taskRunning.Load()),
		CollectPointTotal:  a.table.EnabledTotal(),
		CollectPointOnline: online,
		LastCollectTime:    lastCollect.UnixMilli(),
		CollectInterval:    int(a.collectInterval.Load()),
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		a.logger.Error("序列化 status 消息失败", zap.Error(err))
		return
	}

	topic := a.cfg.Collect.Topic.StatusTopic
	if err := a.mqttClient.Publish(topic, payload); err != nil {
		a.logger.Error("发布 status 消息失败", zap.String("topic", topic), zap.Error(err))
	}
}

// subscribeCmd 订阅平台指令主题
func (a *App) subscribeCmd() error {
	topic := a.cfg.Collect.Topic.CmdTopic
	return a.mqttClient.Subscribe(topic, a.handleCmd)
}

// handleCmd 处理平台下发的指令
func (a *App) handleCmd(topic string, payload []byte) {
	var cmd CmdMessage
	if err := json.Unmarshal(payload, &cmd); err != nil {
		a.logger.Error("解析指令消息失败",
			zap.String("topic", topic),
			zap.Error(err),
		)
		return
	}

	a.logger.Info("收到平台指令",
		zap.String("request_id", cmd.RequestID),
		zap.Any("params", cmd.Params),
	)

	for field, rawVal := range cmd.Params {
		switch field {
		case "collectInterval":
			val, ok := toInt(rawVal)
			if !ok || val < 1 {
				a.logger.Warn("collectInterval 参数非法，已忽略",
					zap.String("request_id", cmd.RequestID),
					zap.Any("value", rawVal),
				)
				continue
			}
			a.collectInterval.Store(int32(val))
			a.logger.Info("采集间隔已更新",
				zap.String("request_id", cmd.RequestID),
				zap.Int("collect_interval_s", val),
			)

		case "taskControl":
			val, ok := toInt(rawVal)
			if !ok || (val != 0 && val != 1) {
				a.logger.Warn("taskControl 参数非法，已忽略",
					zap.String("request_id", cmd.RequestID),
					zap.Any("value", rawVal),
				)
				continue
			}
			a.taskRunning.Store(int32(val))
			state := "停止"
			if val == 1 {
				state = "启动"
			}
			a.logger.Info("采集任务控制",
				zap.String("request_id", cmd.RequestID),
				zap.String("action", state),
			)

		default:
			a.logger.Warn("收到未知指令字段",
				zap.String("request_id", cmd.RequestID),
				zap.String("field", field),
			)
		}
	}

	// 指令执行完成后立即上报状态
	a.publishStatus()
}

// ===== 工具函数 =====

// toInt 将 interface{} 转为 int（兼容 json.Number 和 float64）
func toInt(v interface{}) (int, bool) {
	switch val := v.(type) {
	case float64:
		return int(val), true
	case int:
		return val, true
	case int64:
		return int(val), true
	case json.Number:
		n, err := val.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

func splitSlice[T any](s []T, size int) [][]T {
	var result [][]T
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		result = append(result, s[i:end])
	}
	return result
}
