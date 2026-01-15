package pos

import (
	"encoding/json"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Message type constants
const (
	MessageTypeBlockAssignment   = "block_assignment"
	MessageTypeEpochSchedule     = "epoch_schedule"
	MessageTypeHeartbeat         = "heartbeat"
	MessageTypeValidatorRegister = "validator_register"
	MessageTypeValidatorStatus   = "validator_status"
)

// BlockAssignmentMessage block assignment message
type BlockAssignmentMessage struct {
	Type       string          `json:"type"`
	Assignment BlockAssignment `json:"assignment"`
}

// EpochScheduleMessage epoch schedule message
type EpochScheduleMessage struct {
	Type     string        `json:"type"`
	Schedule EpochSchedule `json:"schedule"`
}

// HeartbeatMessage heartbeat message
type HeartbeatMessage struct {
	Type        string    `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	BlockNumber uint64    `json:"block_number"`
	Epoch       uint64    `json:"epoch"`
}

// ValidatorRegisterMessage validator registration message
type ValidatorRegisterMessage struct {
	Type    string         `json:"type"`
	Address common.Address `json:"address"`
}

// ValidatorStatusMessage validator status message
// Note: Address field is automatically identified by manager based on connection, node doesn't need to send it
type ValidatorStatusMessage struct {
	Type        string `json:"type"`
	BlockNumber uint64 `json:"block_number"`
	Status      string `json:"status"` // "ready", "mining", "done"
}

// BlockAssignment block assignment information
type BlockAssignment struct {
	BlockNumber uint64         `json:"block_number"`
	Epoch       uint64         `json:"epoch"`
	Validator   common.Address `json:"validator"`
	Timestamp   time.Time      `json:"timestamp"`
}

// EpochSchedule epoch schedule
type EpochSchedule struct {
	Epoch       uint64                    `json:"epoch"`
	StartBlock  uint64                    `json:"start_block"`
	EndBlock    uint64                    `json:"end_block"`
	Assignments []BlockAssignment         `json:"assignments"`
	Validators  map[common.Address]uint64 `json:"validators"` // validator address -> number of assigned blocks
	CreatedAt   time.Time                 `json:"created_at"`
}

// ParseMessage parses a message (for testing)
func ParseMessage(data []byte) (interface{}, error) {
	var base struct {
		Type string `json:"type"`
	}

	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}

	switch base.Type {
	case MessageTypeBlockAssignment:
		var msg BlockAssignmentMessage
		err := json.Unmarshal(data, &msg)
		return msg, err
	case MessageTypeEpochSchedule:
		var msg EpochScheduleMessage
		err := json.Unmarshal(data, &msg)
		return msg, err
	case MessageTypeHeartbeat:
		var msg HeartbeatMessage
		err := json.Unmarshal(data, &msg)
		return msg, err
	case MessageTypeValidatorRegister:
		var msg ValidatorRegisterMessage
		err := json.Unmarshal(data, &msg)
		return msg, err
	case MessageTypeValidatorStatus:
		var msg ValidatorStatusMessage
		err := json.Unmarshal(data, &msg)
		return msg, err
	default:
		return nil, nil
	}
}
