package pos

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Message type constants
const (
	MessageTypeBlockAssignment   = "block_assignment"
	MessageTypeEpochSchedule     = "epoch_schedule"
	MessageTypeHeartbeat         = "heartbeat"
	MessageTypeNodeRegister      = "node_register" // Unified registration message for both validators and voters
	MessageTypeValidatorStatus   = "validator_status"
	MessageTypeVoteReward        = "vote_reward"
	MessageTypeVoteRequest       = "vote_request"
	MessageTypeVoteRewardRequest = "vote_reward_request"
	MessageTypeBlockVote         = "block_vote"
	MessageTypeRegisterAck       = "register_ack"
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

// NodeRegisterMessage node registration message (for both validators and voters)
type NodeRegisterMessage struct {
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
	Validators  map[common.Address]uint64 `json:"validators"`
	CreatedAt   time.Time                 `json:"created_at"`
}

// VoteRewardMessage vote reward message from manager
type VoteRewardMessage struct {
	Type        string                   `json:"type"`
	TargetBlock uint64                   `json:"target_block"`
	VotedBlock  uint64                   `json:"voted_block"`
	Rewards     []VoteRewardDistribution `json:"rewards"`
}

// VoteRewardDistribution vote reward distribution for a single voter
type VoteRewardDistribution struct {
	Voter  common.Address `json:"voter"`
	Amount string         `json:"amount"`
}

// VoteRequestMessage vote request message from manager to voting nodes
type VoteRequestMessage struct {
	Type         string    `json:"type"`
	BlockNumber  uint64    `json:"block_number"`
	Timestamp    time.Time `json:"timestamp"`
	VoteDeadline time.Time `json:"vote_deadline"`
}

// BlockVoteMessage block vote message from voting nodes to manager
type BlockVoteMessage struct {
	Type        string         `json:"type"`
	Voter       common.Address `json:"voter"`
	BlockNumber uint64         `json:"block_number"`
	BlockHash   common.Hash    `json:"block_hash"`
	Timestamp   time.Time      `json:"timestamp"`
}

// VoteRewardRequestMessage vote reward request message from nodes to manager
type VoteRewardRequestMessage struct {
	Type       string `json:"type"`
	StartBlock uint64 `json:"start_block"`
}
