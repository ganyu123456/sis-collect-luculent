package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/sis-collect/config"
	"github.com/sis-collect/internal/app"
)

var configPath = flag.String("config", "config.yaml", "配置文件路径")

func main() {
	flag.Parse()

	// 先用基础 logger 启动，配置加载后换成正式 logger
	bootstrapLogger, _ := zap.NewDevelopment()

	cfg, err := config.Load(*configPath)
	if err != nil {
		bootstrapLogger.Fatal("加载配置失败", zap.Error(err))
	}

	logger := buildLogger(cfg.Log.Level, cfg.Log.FilePath)
	defer logger.Sync() //nolint:errcheck

	logger.Info("SIS 数采应用启动",
		zap.String("config", *configPath),
		zap.String("sis_base_url", cfg.SIS.BaseURL),
		zap.String("mqtt_broker", cfg.MQTT.Broker),
		zap.String("device_id", cfg.Collect.Topic.DeviceID),
	)

	// 初始化应用
	application, err := app.New(cfg, logger)
	if err != nil {
		logger.Fatal("应用初始化失败", zap.Error(err))
	}

	// 启动健康检查 HTTP 服务（后台）
	go application.ServeHealth(cfg.Health.Port)

	// 捕获退出信号
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("收到退出信号，正在优雅停止", zap.String("signal", sig.String()))
		cancel()
	}()

	// 阻塞运行主采集循环
	if err := application.Run(ctx); err != nil {
		logger.Error("应用运行异常退出", zap.Error(err))
		os.Exit(1)
	}

	logger.Info("SIS 数采应用已退出")
}

func buildLogger(level string, filePath string) *zap.Logger {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		zapLevel = zapcore.InfoLevel
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "time"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	consoleEncoder := zapcore.NewConsoleEncoder(encoderCfg)

	var cores []zapcore.Core
	cores = append(cores, zapcore.NewCore(consoleEncoder, zapcore.Lock(os.Stdout), zapLevel))

	if filePath != "" {
		f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "无法打开日志文件 %s: %v\n", filePath, err)
		} else {
			jsonEncoder := zapcore.NewJSONEncoder(encoderCfg)
			cores = append(cores, zapcore.NewCore(jsonEncoder, zapcore.AddSync(f), zapLevel))
		}
	}

	core := zapcore.NewTee(cores...)
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
}
