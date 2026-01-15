package pos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	gorillaWS "github.com/gorilla/websocket"
)

const (
	// Reconnection related constants
	reconnectInterval = 5 * time.Second
	maxReconnectDelay = 60 * time.Second
	writeWait         = 10 * time.Second
	pongWait          = 60 * time.Second
	pingPeriod        = (pongWait * 9) / 10
	maxMessageSize    = 512 * 1024
)

type ManagerClient struct {
	managerURL    string
	validatorAddr common.Address // validator address for registration
	log           log.Logger

	conn       *gorillaWS.Conn
	connMu     sync.RWMutex
	connCtx    context.Context
	connCancel context.CancelFunc

	messageHandlers map[string]func([]byte) error
	handlerMu       sync.RWMutex

	connected      bool
	connectedMu    sync.RWMutex
	reconnectDelay time.Duration

	onBlockAssignment func(assignment BlockAssignment) error
	onEpochSchedule   func(schedule EpochSchedule) error
	onHeartbeat       func(heartbeat HeartbeatMessage) error

	currentBlockNumber uint64
	currentEpoch       uint64
	fallbackMode       bool
	lastHeartbeat      time.Time
	stateMu            sync.RWMutex
}

// NewManagerClient creates a new manager client
func NewManagerClient(managerURL string, validatorAddr common.Address, log log.Logger) *ManagerClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &ManagerClient{
		managerURL:      managerURL,
		validatorAddr:   validatorAddr,
		log:             log,
		connCtx:         ctx,
		connCancel:      cancel,
		messageHandlers: make(map[string]func([]byte) error),
		reconnectDelay:  reconnectInterval,
	}
}

// SetOnBlockAssignment sets the block assignment callback
func (c *ManagerClient) SetOnBlockAssignment(fn func(assignment BlockAssignment) error) {
	c.onBlockAssignment = fn
}

// SetOnEpochSchedule sets the epoch schedule callback
func (c *ManagerClient) SetOnEpochSchedule(fn func(schedule EpochSchedule) error) {
	c.onEpochSchedule = fn
}

// SetOnHeartbeat sets the heartbeat callback
func (c *ManagerClient) SetOnHeartbeat(fn func(heartbeat HeartbeatMessage) error) {
	c.onHeartbeat = fn
}

// Start starts the client and begins connection and reconnection loop
func (c *ManagerClient) Start(ctx context.Context) error {
	c.log.Info("Starting manager client", "url", c.managerURL)

	go c.connectLoop(ctx)

	return nil
}

func (c *ManagerClient) Stop() {
	c.log.Info("Stopping manager client")
	c.connCancel()

	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()

	c.setConnected(false)
}

func (c *ManagerClient) connectLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.connCtx.Done():
			return
		default:
		}

		if err := c.connect(ctx); err != nil {
			c.log.Error("Failed to connect to manager", "err", err, "retry_in", c.reconnectDelay)
			c.setConnected(false)

			c.setState(func() {
				c.fallbackMode = true
			})

			select {
			case <-time.After(c.reconnectDelay):
				c.reconnectDelay *= 2
				if c.reconnectDelay > maxReconnectDelay {
					c.reconnectDelay = maxReconnectDelay
				}
			case <-ctx.Done():
				return
			case <-c.connCtx.Done():
				return
			}
			continue
		}

		c.reconnectDelay = reconnectInterval
		c.setConnected(true)

		c.setState(func() {
			c.fallbackMode = false
		})

		done := make(chan struct{})
		go c.readPump(done)
		go c.writePump(done)

		select {
		case <-done:
			c.log.Warn("Connection closed, will reconnect")
		case <-ctx.Done():
			return
		case <-c.connCtx.Done():
			return
		}
	}
}

func (c *ManagerClient) connect(ctx context.Context) error {
	u, err := url.Parse(c.managerURL)
	if err != nil {
		return fmt.Errorf("invalid manager URL: %w", err)
	}

	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("unsupported scheme: %s, expected ws or wss", u.Scheme)
	}

	dialer := gorillaWS.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	c.log.Info("Connecting to manager", "url", c.managerURL)
	conn, _, err := dialer.DialContext(ctx, c.managerURL, nil)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	c.log.Info("Connected to manager")

	// Send ValidatorRegister message immediately after successful connection
	if err := c.registerValidator(); err != nil {
		c.log.Warn("Failed to register validator", "err", err)
		// Don't return error, allow connection to continue
	}

	return nil
}

func (c *ManagerClient) disconnect() {
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	c.setConnected(false)
}

func (c *ManagerClient) sendMessage(data []byte) error {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteMessage(gorillaWS.TextMessage, data)
}

func (c *ManagerClient) readPump(done chan struct{}) {
	defer close(done)

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetReadLimit(maxMessageSize)
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if gorillaWS.IsUnexpectedCloseError(err, gorillaWS.CloseGoingAway, gorillaWS.CloseAbnormalClosure) {
				c.log.Error("WebSocket read error", "err", err)
			}
			break
		}

		if err := c.handleMessage(message); err != nil {
			c.log.Error("Failed to handle message", "err", err)
		}
	}
}

// writePump writes messages in a loop (heartbeat)
func (c *ManagerClient) writePump(done chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return
	}

	for {
		select {
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(gorillaWS.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// handleMessage processes received messages
func (c *ManagerClient) handleMessage(data []byte) error {
	// Parse message type
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return fmt.Errorf("failed to unmarshal message base: %w", err)
	}

	switch base.Type {
	case MessageTypeBlockAssignment:
		var msg BlockAssignmentMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal block assignment: %w", err)
		}

		// Manager only sends to validators that should produce blocks, so receiving a message means we should produce a block
		c.log.Info("Received block assignment from manager",
			"block", msg.Assignment.BlockNumber,
			"epoch", msg.Assignment.Epoch,
			"validator", msg.Assignment.Validator.Hex())

		if c.onBlockAssignment != nil {
			if err := c.onBlockAssignment(msg.Assignment); err != nil {
				return fmt.Errorf("block assignment callback error: %w", err)
			}
		}

		// Update state
		c.setState(func() {
			c.currentBlockNumber = msg.Assignment.BlockNumber
			c.currentEpoch = msg.Assignment.Epoch
			c.lastHeartbeat = time.Now()
		})

	case MessageTypeEpochSchedule:
		var msg EpochScheduleMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal epoch schedule: %w", err)
		}

		c.log.Info("Received epoch schedule",
			"epoch", msg.Schedule.Epoch,
			"start_block", msg.Schedule.StartBlock,
			"end_block", msg.Schedule.EndBlock)

		if c.onEpochSchedule != nil {
			if err := c.onEpochSchedule(msg.Schedule); err != nil {
				return fmt.Errorf("epoch schedule callback error: %w", err)
			}
		}

	case MessageTypeHeartbeat:
		var msg HeartbeatMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal heartbeat: %w", err)
		}

		c.log.Debug("Received heartbeat",
			"block", msg.BlockNumber,
			"epoch", msg.Epoch)

		if c.onHeartbeat != nil {
			if err := c.onHeartbeat(msg); err != nil {
				return fmt.Errorf("heartbeat callback error: %w", err)
			}
		}

		// Update heartbeat time
		c.setState(func() {
			c.lastHeartbeat = time.Now()
		})

	default:
		c.log.Debug("Unknown message type", "type", base.Type)
	}

	return nil
}

// registerValidator sends ValidatorRegister message to register the validator
func (c *ManagerClient) registerValidator() error {
	if c.validatorAddr == (common.Address{}) {
		c.log.Warn("Validator address not set, skipping registration")
		return nil
	}

	msg := ValidatorRegisterMessage{
		Type:    MessageTypeValidatorRegister,
		Address: c.validatorAddr,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal register message: %w", err)
	}

	c.log.Info("Registering validator with manager", "address", c.validatorAddr.Hex())
	return c.sendMessage(data)
}

func (c *ManagerClient) SendValidatorStatus(blockNumber uint64, status string) error {
	msg := ValidatorStatusMessage{
		Type:        MessageTypeValidatorStatus,
		BlockNumber: blockNumber,
		Status:      status,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal status message: %w", err)
	}

	return c.sendMessage(data)
}

// IsConnected checks if connected
func (c *ManagerClient) IsConnected() bool {
	c.connectedMu.RLock()
	defer c.connectedMu.RUnlock()
	return c.connected
}

// IsFallbackMode checks if in fallback mode
func (c *ManagerClient) IsFallbackMode() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.fallbackMode
}

// GetCurrentBlockNumber gets the current block number
func (c *ManagerClient) GetCurrentBlockNumber() uint64 {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.currentBlockNumber
}

// GetCurrentEpoch gets the current epoch
func (c *ManagerClient) GetCurrentEpoch() uint64 {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.currentEpoch
}

// setConnected sets the connection state
func (c *ManagerClient) setConnected(connected bool) {
	c.connectedMu.Lock()
	c.connected = connected
	c.connectedMu.Unlock()
}

// setState updates state (thread-safe)
func (c *ManagerClient) setState(fn func()) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	fn()
}
