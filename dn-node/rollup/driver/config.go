package driver

import "time"

type Config struct {
	// VerifierConfDepth is the distance to keep from the L1 head when reading L1 data for core derivation.
	VerifierConfDepth uint64 `json:"verifier_conf_depth"`

	// SequencerConfDepth is the distance to keep from the L1 head as origin when sequencing new core blocks.
	// If this distance is too large, the sequencer may:
	// - not adopt a L1 origin within the allowed time (rollup.Config.MaxSequencerDrift)
	// - not adopt a L1 origin that can be included on L1 within the allowed range (rollup.Config.SeqWindowSize)
	// and thus fail to produce a block with anything more than deposits.
	SequencerConfDepth uint64 `json:"sequencer_conf_depth"`

	// SequencerEnabled is true when the driver should sequence new blocks.
	SequencerEnabled bool `json:"sequencer_enabled"`

	// SequencerStopped is false when the driver should sequence new blocks.
	SequencerStopped bool `json:"sequencer_stopped"`

	// SequencerMaxSafeLag is the maximum number of core blocks for restricting the distance between core safe and unsafe.
	// Disabled if 0.
	SequencerMaxSafeLag uint64 `json:"sequencer_max_safe_lag"`

	// RecoverMode forces the sequencer to select the next L1 Origin exactly, and create an empty block,
	// to be compatible with verifiers forcefully generating the same block while catching up the sequencing window timeout.
	RecoverMode bool `json:"recover_mode"`

	// PosMode indicates if the sequencer is in Proof-of-Stake mode.
	// In PoS mode, the sequencer should not automatically produce blocks,
	// but only produce blocks when instructed by the manager.
	PosMode bool `json:"pos_mode"`

	// PoSActivationBlock is the first block number at which PoS scheduling applies.
	// When 0 or next block >= PoSActivationBlock, PosMode applies (Manager assigns blocks).
	// When > 0 and next block < PoSActivationBlock, only LegacySequencer auto-schedules; other nodes sync only.
	PoSActivationBlock uint64 `json:"pos_activation_block"`

	// LegacySequencer: when true and next block < PoSActivationBlock, this node is the single sequencer (auto-schedule).
	// When false (default), before PoSActivationBlock this node only syncs from others and does not produce blocks.
	// Set true only on the one node that continues the pre-upgrade chain (e.g. validator1).
	LegacySequencer bool `json:"legacy_sequencer"`

	// Maximum number of requests to make per batch
	MaxRequestsPerBatch int `json:"max_requests_per_batch"`

	// EngineCallTimeout is applied to engine-deriver operations that wrap RPC calls
	// (forkchoice+start build, getPayload, newPayload, cancel build) in a context timeout.
	// Must be >= the execution client's per-call timeout (e.g. l2.engine-rpc-timeout); otherwise
	// the shorter outer deadline wins and raising only the RPC timeout has no effect.
	// Zero keeps legacy 10s defaults inside the engine package.
	EngineCallTimeout time.Duration `json:"engine_call_timeout,omitempty"`
}
