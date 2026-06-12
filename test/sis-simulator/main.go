// SIS HTTP API 模拟器
// 模拟真实 SIS 系统的 /api/Values/GetDataPoint 接口，返回随机波动的测点数据
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PointValueBody 与主应用中结构体保持一致
type PointValueBody struct {
	ExpandName    string  `json:"ExpandName"`
	PointDescribe string  `json:"PointDescribe"`
	PointName     string  `json:"PointName"`
	PointTime     string  `json:"PointTime"`
	PointUnit     string  `json:"PointUnit"`
	PointValue    float64 `json:"PointValue"`
}

type PointValue struct {
	Key   string         `json:"Key"`
	Value PointValueBody `json:"Value"`
}

// 测点基础配置：模拟各测点的基准值和单位
var pointConfig = map[string]struct {
	baseValue   float64
	unit        string
	describe    string
	expandName  string
	offlineRate float64 // 离线概率 0~1
}{
	"DDM.SIS.0DCS_00BHT03GT001XQ001": {120.5, "A", "主冷却水泵房变压器A高压断路器高压侧电流", "BHT03GT001_I", 0.02},
	"DDM.SIS.0DCS_00BHT03GT001XQ002": {850.0, "KW", "主冷却水泵房变压器A有功功率", "BHT03GT001_P", 0.02},
	"DDM.SIS.0DCS_00BHT03GT001XQ003": {12500.0, "kWh", "主冷却水泵房变压器A有功电度", "BHT03GT001_E", 0.01},
	"DDM.SIS.0DCS_00BHT03GT002XQ001": {98.3, "A", "循环水PCA段电源进线B相电流", "BHT03GT002_I", 0.02},
	"DDM.SIS.0DCS_00BHT04GT001XQ001": {115.8, "A", "主冷却水泵房变压器B高压断路器高压侧电流", "BHT04GT001_I", 0.02},
	"DDM.SIS.0DCS_00BHT04GT001XQ002": {780.0, "KW", "主冷却水泵房变压器B有功功率", "BHT04GT001_P", 0.02},
	"DDM.SIS.0DCS_00BHT04GT001XQ003": {11800.0, "kWh", "主冷却水泵房变压器B有功电度", "BHT04GT001_E", 0.01},
	"DDM.SIS.0DCS_00BHT04GT002XQ001": {102.1, "A", "循环水PCB段电源进线B相电流", "BHT04GT002_I", 0.02},
	"DDM.SIS.0DCS_00BHT05GT001XQ001": {45.6, "A", "办公楼变压器A高压断路器高压侧电流", "BHT05GT001_I", 0.05},
	"DDM.SIS.0DCS_00BHT05GT001XQ002": {320.0, "KW", "办公楼变压器A有功功率", "BHT05GT001_P", 0.05},
}

// 用于模拟值随时间缓慢漂移
var (
	mu         sync.Mutex
	driftState = make(map[string]float64) // 每个测点当前漂移量
	callCount  int
)

func getPointValue(name string) (float64, bool) {
	mu.Lock()
	defer mu.Unlock()

	cfg, ok := pointConfig[name]
	if !ok {
		// 未知测点：生成随机值
		cfg.baseValue = 100.0
		cfg.offlineRate = 0.1
	}

	// 5% 概率模拟离线（不返回该测点）
	if rand.Float64() < cfg.offlineRate {
		return 0, false
	}

	// 漂移：每次调用随机小幅波动，模拟真实传感器
	drift := driftState[name]
	drift += (rand.Float64()*2 - 1) * cfg.baseValue * 0.005 // ±0.5% 漂移
	// 限制漂移范围在 ±10% 以内
	maxDrift := cfg.baseValue * 0.10
	drift = math.Max(-maxDrift, math.Min(maxDrift, drift))
	driftState[name] = drift

	value := cfg.baseValue + drift
	// 加入小幅噪声
	noise := (rand.Float64()*2 - 1) * cfg.baseValue * 0.002
	value += noise

	return math.Round(value*100) / 100, true
}

func handleGetDataPoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Names string `json:"Names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	names := strings.Split(req.Names, ",")
	results := make([]*PointValue, 0, len(names))

	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		value, online := getPointValue(name)
		if !online {
			log.Printf("[OFFLINE] 测点离线，跳过: %s", name)
			continue
		}

		cfg := pointConfig[name]
		results = append(results, &PointValue{
			Key: name,
			Value: PointValueBody{
				ExpandName:    cfg.expandName,
				PointDescribe: cfg.describe,
				PointName:     name,
				PointTime:     time.Now().Format("2006-01-02T15:04:05"),
				PointUnit:     cfg.unit,
				PointValue:    value,
			},
		})
	}

	mu.Lock()
	callCount++
	count := callCount
	mu.Unlock()

	log.Printf("[#%d] 请求 %d 个测点 → 返回 %d 个在线测点", count, len(names), len(results))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	count := callCount
	mu.Unlock()
	fmt.Fprintf(w, `{"status":"ok","total_calls":%d}`, count)
}

func main() {
	rand.New(rand.NewSource(time.Now().UnixNano()))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/Values/GetDataPoint", handleGetDataPoint)
	mux.HandleFunc("/health", handleHealth)

	addr := ":7757"
	log.Printf("SIS 模拟器启动，监听 %s", addr)
	log.Printf("支持的测点数量: %d", len(pointConfig))
	log.Printf("接口: POST /api/Values/GetDataPoint")

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
