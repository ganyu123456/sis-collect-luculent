// Package collector 负责向 SIS 系统 HTTP 接口拉取测点实时数据
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sis-collect-luculent/config"
	"go.uber.org/zap"
)

// PointValue SIS 接口返回的单个测点数据
type PointValue struct {
	Key   string         `json:"Key"`
	Value PointValueBody `json:"Value"`
}

// PointValueBody 测点详细字段
type PointValueBody struct {
	ExpandName   string  `json:"ExpandName"`
	PointDescribe string `json:"PointDescribe"`
	PointName    string  `json:"PointName"`
	PointTime    string  `json:"PointTime"`
	PointUnit    string  `json:"PointUnit"`
	PointValue   float64 `json:"PointValue"`
}

// CollectResult 一次采集任务的结果
type CollectResult struct {
	// Points 所有成功采集的测点数据，key=PointName
	Points map[string]*PointValue
	// OnlineCount 成功采集数
	OnlineCount int
	// TotalRequested 本次请求总测点数
	TotalRequested int
	// CollectTime 本次采集时间
	CollectTime time.Time
	// Errors 分批请求中产生的错误
	Errors []error
}

// Collector SIS HTTP 采集器
type Collector struct {
	cfg    *config.SISConfig
	client *http.Client
	logger *zap.Logger
}

// New 创建采集器
func New(cfg *config.SISConfig, logger *zap.Logger) *Collector {
	return &Collector{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		},
		logger: logger,
	}
}

// FetchBatch 分批请求 SIS，将 pointNames 按 batchSize 拆分，并发或串行请求后合并结果
func (c *Collector) FetchBatch(ctx context.Context, pointNames []string) *CollectResult {
	result := &CollectResult{
		Points:         make(map[string]*PointValue),
		TotalRequested: len(pointNames),
		CollectTime:    time.Now(),
	}

	batchSize := c.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 50
	}

	// 将 pointNames 拆分为多个批次
	batches := splitBatch(pointNames, batchSize)
	c.logger.Debug("开始分批采集 SIS 数据",
		zap.Int("total_points", len(pointNames)),
		zap.Int("batch_size", batchSize),
		zap.Int("batch_count", len(batches)),
	)

	for batchIdx, batch := range batches {
		points, err := c.fetchOneBatch(ctx, batch)
		if err != nil {
			c.logger.Warn("批次请求失败",
				zap.Int("batch_index", batchIdx),
				zap.Int("batch_size", len(batch)),
				zap.Error(err),
			)
			result.Errors = append(result.Errors, fmt.Errorf("批次[%d]: %w", batchIdx, err))
			continue
		}
		for _, pv := range points {
			result.Points[pv.Key] = pv
		}
		result.OnlineCount += len(points)
	}

	return result
}

// fetchOneBatch 请求单批测点数据
func (c *Collector) fetchOneBatch(ctx context.Context, names []string) ([]*PointValue, error) {
	// 构造请求体：{"Names":"name1,name2,..."}
	namesStr := strings.Join(names, ",")
	body, _ := json.Marshal(map[string]string{"Names": namesStr})

	url := c.cfg.BaseURL + c.cfg.DataPointPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP 响应状态异常: %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	return parseResponse(respBody)
}

// parseResponse 解析 SIS 接口响应
// SIS 返回两种格式：
//  1. 直接 JSON 数组：[{"Key":"...","Value":{...}},...]
//  2. 转义字符串（外层带引号）："[{\"Key\":\"...\"}]"
func parseResponse(data []byte) ([]*PointValue, error) {
	raw := strings.TrimSpace(string(data))

	// 处理外层字符串包裹的情况
	if len(raw) > 0 && raw[0] == '"' {
		var inner string
		if err := json.Unmarshal(data, &inner); err != nil {
			return nil, fmt.Errorf("解析外层字符串失败: %w", err)
		}
		raw = inner
	}

	var result []*PointValue
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("解析测点数据失败: %w", err)
	}
	return result, nil
}

// splitBatch 将切片按 size 分批
func splitBatch(names []string, size int) [][]string {
	var batches [][]string
	for i := 0; i < len(names); i += size {
		end := i + size
		if end > len(names) {
			end = len(names)
		}
		batches = append(batches, names[i:end])
	}
	return batches
}
