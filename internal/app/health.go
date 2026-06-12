package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// HealthResponse /health 接口响应体
type HealthResponse struct {
	Status          string    `json:"status"`           // "ok" / "degraded"
	TaskRunning     bool      `json:"task_running"`
	MQTTConnected   bool      `json:"mqtt_connected"`
	CollectInterval int       `json:"collect_interval_s"`
	PointTotal      int       `json:"point_total"`
	PointEnabled    int       `json:"point_enabled"`
	OnlineCount     int       `json:"online_count"`
	LastCollectTime time.Time `json:"last_collect_time"`
	Uptime          string    `json:"uptime"`
	Version         string    `json:"version"`
}

var (
	startTime = time.Now()
	// Version 由 ldflags 注入，如: -ldflags "-X github.com/sis-collect-luculent/internal/app.Version=v1.0.0"
	Version = "dev"
)

// ServeHealth 启动 HTTP 健康检查服务器（阻塞）
func (a *App) ServeHealth(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.healthHandler)
	mux.HandleFunc("/ready", a.readyHandler)

	addr := fmt.Sprintf(":%d", port)
	a.logger.Info("健康检查 HTTP 服务启动", zap.String("addr", addr))

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		a.logger.Error("健康检查 HTTP 服务异常退出", zap.Error(err))
	}
}

func (a *App) healthHandler(w http.ResponseWriter, _ *http.Request) {
	s := a.GetStatus()

	status := "ok"
	httpCode := http.StatusOK
	if !s.MQTTConnected {
		status = "degraded"
		httpCode = http.StatusServiceUnavailable
	}

	resp := HealthResponse{
		Status:          status,
		TaskRunning:     s.TaskRunning,
		MQTTConnected:   s.MQTTConnected,
		CollectInterval: s.CollectInterval,
		PointTotal:      s.PointTotal,
		PointEnabled:    s.PointEnabled,
		OnlineCount:     s.OnlineCount,
		LastCollectTime: s.LastCollectTime,
		Uptime:          time.Since(startTime).Round(time.Second).String(),
		Version:         Version,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(resp)
}

// readyHandler /ready 就绪探针：MQTT 已连接才算就绪
func (a *App) readyHandler(w http.ResponseWriter, _ *http.Request) {
	if !a.mqttClient.IsConnected() {
		http.Error(w, `{"ready":false}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"ready":true}`)
}
