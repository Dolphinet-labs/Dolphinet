package pos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	writeMu    sync.Mutex // Protects WebSocket write operations (not concurrent-safe)
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
	onVoteReward      func(reward VoteRewardMessage) error
	onVoteRequest     func(request VoteRequestMessage) error
	onRegisterAck     func(status string) error
	onBootnodesUpdate func(bootnodes []string) error

	bootnodesProvider func() []string

	currentBlockNumber uint64
	currentEpoch       uint64
	fallbackMode       bool
	lastHeartbeat      time.Time
	stateMu            sync.RWMutex

	registeredThisSession bool
	registeredMu          sync.Mutex
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

// SetOnVoteReward sets the vote reward callback
func (c *ManagerClient) SetOnVoteReward(fn func(reward VoteRewardMessage) error) {
	c.onVoteReward = fn
}

// SetOnVoteRequest sets the vote request callback
func (c *ManagerClient) SetOnVoteRequest(fn func(request VoteRequestMessage) error) {
	c.onVoteRequest = fn
}

// SetOnRegisterAck sets the register ack callback
func (c *ManagerClient) SetOnRegisterAck(fn func(status string) error) {
	c.onRegisterAck = fn
}

// SetOnBootnodesUpdate sets the bootnodes update callback.
func (c *ManagerClient) SetOnBootnodesUpdate(fn func(bootnodes []string) error) {
	c.onBootnodesUpdate = fn
}

// SetBootnodesProvider sets a function that returns ENR/enode records to include in registration.
func (c *ManagerClient) SetBootnodesProvider(fn func() []string) {
	c.bootnodesProvider = fn
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
	c.registeredMu.Lock()
	c.registeredThisSession = false
	c.registeredMu.Unlock()

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

	// WebSocket connections are not concurrent-safe for writing
	// Use writeMu to serialize all write operations
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

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

		messages := bytes.Split(message, []byte{'\n'})
		for _, msg := range messages {
			if len(msg) == 0 {
				continue // Skip empty messages
			}
			if err := c.handleMessage(msg); err != nil {
				// Log message preview (first 100 bytes) for debugging
				previewLen := len(msg)
				if previewLen > 100 {
					previewLen = 100
				}
				c.log.Error("Failed to handle message", "err", err, "message_preview", string(msg[:previewLen]))
			}
		}
	}
}

// writePump writes messages in a loop (heartbeat)
func (c *ManagerClient) writePump(done chan struct{}) {
	pingTicker := time.NewTicker(pingPeriod)
	heartbeatTicker := time.NewTicker(2 * time.Second) // Send heartbeat every 10 seconds
	defer pingTicker.Stop()
	defer heartbeatTicker.Stop()

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		return
	}

	for {
		select {
		case <-pingTicker.C:
			// WebSocket connections are not concurrent-safe for writing
			// Use writeMu to serialize all write operations
			c.writeMu.Lock()
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			err := conn.WriteMessage(gorillaWS.PingMessage, nil)
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		case <-heartbeatTicker.C:
			c.sendHeartbeat()
		case <-done:
			return
		}
	}
}

func (c *ManagerClient) sendHeartbeat() {
	c.stateMu.RLock()
	blockNumber := c.currentBlockNumber
	epoch := c.currentEpoch
	c.stateMu.RUnlock()

	msg := HeartbeatMessage{
		Type:        MessageTypeHeartbeat,
		Timestamp:   time.Now(),
		BlockNumber: blockNumber,
		Epoch:       epoch,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		c.log.Error("Failed to marshal heartbeat message", "err", err)
		return
	}

	if err := c.sendMessage(data); err != nil {
		c.log.Debug("Failed to send heartbeat", "err", err)
	} else {
		c.log.Debug("Sent heartbeat to manager", "block", blockNumber, "epoch", epoch)
	}
}

// SendBlockVote sends a block vote to the manager
func (c *ManagerClient) SendBlockVote(vote BlockVoteMessage) error {
	data, err := json.Marshal(vote)
	if err != nil {
		c.log.Error("Failed to marshal block vote", "err", err)
		return err
	}

	if err := c.sendMessage(data); err != nil {
		c.log.Error("Failed to send block vote", "err", err)
		return err
	}

	c.log.Debug("Sent block vote to manager", "block", vote.BlockNumber, "hash", vote.BlockHash.Hex())
	return nil
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

	case MessageTypeVoteReward:
		var msg VoteRewardMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal vote reward: %w", err)
		}

		c.log.Info("Received vote reward from manager",
			"target_block", msg.TargetBlock,
			"voted_block", msg.VotedBlock,
			"reward_count", len(msg.Rewards))

		if c.onVoteReward != nil {
			if err := c.onVoteReward(msg); err != nil {
				return fmt.Errorf("vote reward callback error: %w", err)
			}
		}

	case MessageTypeVoteRequest:
		var msg VoteRequestMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal vote request: %w", err)
		}

		c.log.Info("Received vote request from manager",
			"block", msg.BlockNumber,
			"deadline", msg.VoteDeadline)

		if c.onVoteRequest != nil {
			if err := c.onVoteRequest(msg); err != nil {
				return fmt.Errorf("vote request callback error: %w", err)
			}
		}

	case MessageTypeRegisterAck:
		var msg struct {
			Type    string `json:"type"`
			Address string `json:"address"`
			Status  string `json:"status"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal register ack: %w", err)
		}

		c.log.Info("Received register ack from manager",
			"address", msg.Address,
			"status", msg.Status)

		if c.onRegisterAck != nil {
			if err := c.onRegisterAck(msg.Status); err != nil {
				return fmt.Errorf("register ack callback error: %w", err)
			}
		}

	case MessageTypeBootnodesUpdate:
		var msg BootnodesUpdateMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("failed to unmarshal bootnodes update: %w", err)
		}
		c.log.Info("Received bootnodes update from manager", "count", len(msg.Bootnodes))
		if c.onBootnodesUpdate != nil {
			if err := c.onBootnodesUpdate(msg.Bootnodes); err != nil {
				return fmt.Errorf("bootnodes update callback error: %w", err)
			}
		}

	default:
		c.log.Debug("Unknown message type", "type", base.Type)
	}

	return nil
}

func (c *ManagerClient) RegisterWithBlockNumber(nextBlockNumber uint64) error {
	if c.validatorAddr == (common.Address{}) {
		c.log.Warn("Validator address not set, skipping registration")
		return nil
	}
	c.registeredMu.Lock()
	if c.registeredThisSession {
		c.registeredMu.Unlock()
		return nil
	}
	c.registeredMu.Unlock()

	var bootnodes []string
	if c.bootnodesProvider != nil {
		bootnodes = c.bootnodesProvider()
	}
	msg := NodeRegisterMessage{
		Type:               MessageTypeNodeRegister,
		Address:            c.validatorAddr,
		CurrentBlockNumber: nextBlockNumber,
		Bootnodes:          bootnodes,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal register message: %w", err)
	}

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}

	c.writeMu.Lock()
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	err = conn.WriteMessage(gorillaWS.TextMessage, data)
	c.writeMu.Unlock()
	if err != nil {
		return err
	}

	c.registeredMu.Lock()
	c.registeredThisSession = true
	c.registeredMu.Unlock()
	c.log.Info("Registered node with manager", "address", c.validatorAddr.Hex(), "next_block_number", nextBlockNumber)
	return nil
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

// RequestVoteRewards requests vote rewards for a block range from manager
func (c *ManagerClient) RequestVoteRewards(startBlock uint64) error {
	msg := VoteRewardRequestMessage{
		Type:       MessageTypeVoteRewardRequest,
		StartBlock: startBlock,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal vote reward request message: %w", err)
	}

	c.log.Info("Requesting vote rewards from manager", "start_block", startBlock)
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
