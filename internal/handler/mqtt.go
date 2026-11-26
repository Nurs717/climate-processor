package handler

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"climate-processor/internal/service"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTConfig holds everything needed to connect to the broker.
type MQTTConfig struct {
	BrokerURL string // tcp://host:1883 or ssl://host:8883
	ClientID  string
	Topic     string

	Username string // optional
	Password string

	CAFile        string // optional PEM file with the CA that signed the broker certificate
	TLSServerName string // optional name expected in the broker certificate
}

type MQTTHandler struct {
	log     *slog.Logger
	client  mqtt.Client
	service *service.ClimateService
	topic   string
}

func NewMQTTHandler(log *slog.Logger, cfg MQTTConfig, svc *service.ClimateService) (*MQTTHandler, error) {
	// Route the paho library's own diagnostics (wrong password, TLS errors, ...) through our logger.
	// Its warnings are internal noise ("memorystore wiped" on every start), so they are logged at
	// debug level; real connection problems are reported by our own connect/lost handlers and by
	// paho's errors.
	pahoLog := log.With("library", "paho").Handler()
	mqtt.ERROR = slog.NewLogLogger(pahoLog, slog.LevelError)
	mqtt.WARN = slog.NewLogLogger(pahoLog, slog.LevelDebug)

	h := &MQTTHandler{log: log, service: svc, topic: cfg.Topic}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(cfg.BrokerURL)
	opts.SetClientID(cfg.ClientID)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}
	if cfg.CAFile != "" {
		tlsCfg, err := newTLSConfig(cfg.CAFile, cfg.TLSServerName)
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
	}

	// The broker keeps no state for us: we re-subscribe on every (re)connect, see the OnConnect handler.
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true) // keep retrying if the broker is not up yet
	opts.SetConnectRetryInterval(5 * time.Second)
	opts.SetMaxReconnectInterval(time.Minute)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetPingTimeout(10 * time.Second)

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		h.log.Info("connected to the MQTT broker")
		// Subscribing here (not once at startup) is what keeps data flowing after a broker restart.
		token := c.Subscribe(h.topic, 1, h.onMessage)
		if token.Wait() && token.Error() != nil {
			h.log.Error("subscription failed", "topic", h.topic, "error", token.Error())
			return
		}
		h.log.Info("subscribed", "topic", h.topic)
	})
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		h.log.Warn("lost connection to the MQTT broker, waiting for auto-reconnect", "error", err)
	})
	opts.SetReconnectingHandler(func(c mqtt.Client, o *mqtt.ClientOptions) {
		h.log.Info("reconnecting to the MQTT broker")
	})

	h.client = mqtt.NewClient(opts)
	return h, nil
}

// Connect starts connecting in the background and returns immediately: with ConnectRetry the
// client keeps trying until the broker is reachable, and the OnConnect handler subscribes.
func (h *MQTTHandler) Connect() {
	_ = h.client.Connect()
}

func (h *MQTTHandler) onMessage(_ mqtt.Client, msg mqtt.Message) {
	// Pass the payload (byte slice) to the business logic layer; rejects are logged there.
	_ = h.service.ProcessMessage(msg.Payload())
}

// Disconnect gracefully closes the connection on application shutdown
func (h *MQTTHandler) Disconnect() {
	h.client.Disconnect(250) // Wait 250ms for in-flight messages to complete
	h.log.Info("MQTT client disconnected")
}

func newTLSConfig(caFile, serverName string) (*tls.Config, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read MQTT CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("MQTT CA file contains no valid PEM certificate")
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: serverName, // empty = use the host from the broker URL
		MinVersion: tls.VersionTLS12,
	}, nil
}
