// Package mqtt 封装 MQTT 客户端，提供发布/订阅能力及指数退避自动重连
package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"

	"github.com/sis-collect/config"
)

// MessageHandler 订阅消息回调
type MessageHandler func(topic string, payload []byte)

// Client MQTT 客户端封装
type Client struct {
	cfg       *config.MQTTConfig
	logger    *zap.Logger
	paho      pahomqtt.Client
	connected atomic.Bool

	// 订阅表：topic -> handler，用于重连后自动恢复订阅
	subsMu  sync.RWMutex
	subs    map[string]MessageHandler

	// 重连控制
	reconnectStop chan struct{}
}

// New 创建并连接 MQTT 客户端
func New(cfg *config.MQTTConfig, logger *zap.Logger) (*Client, error) {
	c := &Client{
		cfg:           cfg,
		logger:        logger,
		subs:          make(map[string]MessageHandler),
		reconnectStop: make(chan struct{}),
	}

	opts, err := c.buildOptions()
	if err != nil {
		return nil, err
	}

	c.paho = pahomqtt.NewClient(opts)
	if token := c.paho.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("MQTT 初始连接失败: %w", token.Error())
	}

	c.connected.Store(true)
	logger.Info("MQTT 连接成功", zap.String("broker", cfg.Broker), zap.String("client_id", cfg.ClientID))
	return c, nil
}

// Publish 发布消息（QoS 使用配置值，retain=false）
func (c *Client) Publish(topic string, payload []byte) error {
	if !c.connected.Load() {
		return fmt.Errorf("MQTT 未连接，无法发布消息到主题 %s", topic)
	}
	token := c.paho.Publish(topic, c.cfg.QoS, false, payload)
	if token.Wait() && token.Error() != nil {
		return fmt.Errorf("发布失败 [%s]: %w", topic, token.Error())
	}
	return nil
}

// Subscribe 订阅主题
func (c *Client) Subscribe(topic string, handler MessageHandler) error {
	c.subsMu.Lock()
	c.subs[topic] = handler
	c.subsMu.Unlock()

	if !c.connected.Load() {
		return nil // 重连后会自动恢复订阅
	}
	return c.doSubscribe(topic, handler)
}

// IsConnected 返回当前连接状态
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// Disconnect 断开连接
func (c *Client) Disconnect() {
	close(c.reconnectStop)
	c.paho.Disconnect(1000)
	c.connected.Store(false)
	c.logger.Info("MQTT 客户端已断开")
}

// ===== 内部方法 =====

func (c *Client) buildOptions() (*pahomqtt.ClientOptions, error) {
	opts := pahomqtt.NewClientOptions()
	opts.AddBroker(c.cfg.Broker)
	opts.SetClientID(c.cfg.ClientID)

	if c.cfg.Username != "" {
		opts.SetUsername(c.cfg.Username)
	}
	if c.cfg.Password != "" {
		opts.SetPassword(c.cfg.Password)
	}

	opts.SetKeepAlive(time.Duration(c.cfg.KeepAlive) * time.Second)
	opts.SetCleanSession(c.cfg.CleanSession)
	opts.SetAutoReconnect(false) // 由我们自己管理重连（指数退避）

	if c.cfg.TLSEnable {
		tlsCfg, err := buildTLSConfig(c.cfg)
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
	}

	opts.SetConnectionLostHandler(func(_ pahomqtt.Client, err error) {
		c.connected.Store(false)
		c.logger.Warn("MQTT 连接断开，启动重连", zap.Error(err))
		go c.reconnectLoop()
	})

	opts.SetOnConnectHandler(func(_ pahomqtt.Client) {
		c.connected.Store(true)
		c.logger.Info("MQTT 已连接/重连成功")
		c.restoreSubscriptions()
	})

	return opts, nil
}

// reconnectLoop 指数退避重连
func (c *Client) reconnectLoop() {
	minDelay := time.Duration(c.cfg.ReconnectMinSeconds) * time.Second
	maxDelay := time.Duration(c.cfg.ReconnectMaxSeconds) * time.Second
	attempt := 0

	for {
		select {
		case <-c.reconnectStop:
			return
		default:
		}

		// 指数退避：delay = min(min*2^attempt, max)
		delay := time.Duration(float64(minDelay) * math.Pow(2, float64(attempt)))
		if delay > maxDelay {
			delay = maxDelay
		}

		c.logger.Info("准备重连 MQTT",
			zap.Duration("delay", delay),
			zap.Int("attempt", attempt+1),
		)
		time.Sleep(delay)

		token := c.paho.Connect()
		if token.Wait() && token.Error() == nil {
			c.logger.Info("MQTT 重连成功", zap.Int("attempt", attempt+1))
			return
		}
		c.logger.Warn("MQTT 重连失败",
			zap.Int("attempt", attempt+1),
			zap.Error(token.Error()),
		)
		attempt++
	}
}

// restoreSubscriptions 重连后恢复所有订阅
func (c *Client) restoreSubscriptions() {
	c.subsMu.RLock()
	defer c.subsMu.RUnlock()

	for topic, handler := range c.subs {
		if err := c.doSubscribe(topic, handler); err != nil {
			c.logger.Error("恢复订阅失败", zap.String("topic", topic), zap.Error(err))
		} else {
			c.logger.Info("恢复订阅成功", zap.String("topic", topic))
		}
	}
}

func (c *Client) doSubscribe(topic string, handler MessageHandler) error {
	token := c.paho.Subscribe(topic, c.cfg.QoS, func(_ pahomqtt.Client, msg pahomqtt.Message) {
		handler(msg.Topic(), msg.Payload())
	})
	if token.Wait() && token.Error() != nil {
		return fmt.Errorf("订阅失败 [%s]: %w", topic, token.Error())
	}
	return nil
}

func buildTLSConfig(cfg *config.MQTTConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{}

	if cfg.TLSCACert != "" {
		caCert, err := os.ReadFile(cfg.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("读取 CA 证书失败: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("加载 CA 证书失败")
		}
		tlsCfg.RootCAs = pool
	}

	if cfg.TLSClientCert != "" && cfg.TLSClientKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSClientCert, cfg.TLSClientKey)
		if err != nil {
			return nil, fmt.Errorf("加载客户端证书失败: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}
