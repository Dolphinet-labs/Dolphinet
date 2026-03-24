package flags

import (
	"fmt"
	"github.com/urfave/cli/v2"
	"time"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/engine"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/sync"
	openum "github.com/dolphinnet-labs/dolphinnet/dn-service/enum"
	opflags "github.com/dolphinnet-labs/dolphinnet/dn-service/flags"
	oplog "github.com/dolphinnet-labs/dolphinnet/dn-service/log"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/oppprof"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/sources"
)

// Flags

const EnvVarPrefix = "OP_NODE"

const (
	RollupCategory     = "1. ROLLUP"
	L1RPCCategory      = "2. L1 RPC"
	SequencerCategory  = "3. SEQUENCER"
	OperationsCategory = "4. LOGGING, METRICS, DEBUGGING, AND API"
	P2PCategory        = "5. PEER-TO-PEER"
	AltDACategory      = "6. ALT-DA (EXPERIMENTAL)"
	MiscCategory       = "7. MISC"
	InteropCategory    = "8. INTEROP (SUPER EXPERIMENTAL)"
)

func init() {
	cli.HelpFlag.(*cli.BoolFlag).Category = MiscCategory
	cli.VersionFlag.(*cli.BoolFlag).Category = MiscCategory
}

func prefixEnvVars(names ...string) []string {
	envs := make([]string, 0, len(names))
	for _, name := range names {
		envs = append(envs, EnvVarPrefix+"_"+name)
	}
	return envs
}

var (
	/* Required Flags */
	L2EngineAddr = &cli.StringFlag{
		Name:     "l2",
		Usage:    "Address of core Engine JSON-RPC endpoints to use (engine and eth namespace required)",
		EnvVars:  prefixEnvVars("L2_ENGINE_RPC"),
		Category: RollupCategory,
	}
	L2EngineJWTSecret = &cli.StringFlag{
		Name:        "l2.jwt-secret",
		Usage:       "Path to JWT secret key. Keys are 32 bytes, hex encoded in a file. A new key will be generated if the file is empty.",
		EnvVars:     prefixEnvVars("L2_ENGINE_AUTH"),
		Value:       "",
		Destination: new(string),
		Category:    RollupCategory,
	}
	/* Optional Flags */
	BeaconFallbackAddrs = &cli.StringSliceFlag{
		Name:     "l1.beacon-fallbacks",
		Aliases:  []string{"l1.beacon-archiver"},
		Usage:    "Addresses of L1 Beacon-API compatible HTTP fallback endpoints. Used to fetch blob sidecars not available at the l1.beacon (e.g. expired blobs).",
		EnvVars:  prefixEnvVars("L1_BEACON_FALLBACKS", "L1_BEACON_ARCHIVER"),
		Category: L1RPCCategory,
	}
	SyncModeFlag = &cli.GenericFlag{
		Name:    "syncmode",
		Usage:   fmt.Sprintf("Blockchain sync mode (options: %s)", openum.EnumString(sync.ModeStrings)),
		EnvVars: prefixEnvVars("SYNCMODE"),
		Value: func() *sync.Mode {
			out := sync.CLSync
			return &out
		}(),
		Category: RollupCategory,
	}
	ELRpcUrlFlag = &cli.StringFlag{
		Name:     "el.rpc-url",
		Usage:    "RPC URL for getting L2 information in execution-layer sync mode",
		Value:    "127.0.0.1:9545",
		EnvVars:  prefixEnvVars("EL_RPC_URL"),
		Category: RollupCategory,
	}
	ELRPCRateLimit = &cli.Float64Flag{
		Name:     "el.rpc-rate-limit",
		Usage:    "Optional self-imposed global rate-limit on el RPC requests, specified in requests / second. Disabled if set to 0.",
		EnvVars:  prefixEnvVars("L1_RPC_RATE_LIMIT"),
		Value:    0,
		Category: RollupCategory,
	}
	ELRPCMaxBatchSize = &cli.IntFlag{
		Name:     "el.rpc-max-batch-size",
		Usage:    "Maximum number of RPC requests to bundle, e.g. during el blocks receipt fetching. The el RPC rate limit counts this as N items, but allows it to burst at once.",
		EnvVars:  prefixEnvVars("EL_RPC_MAX_BATCH_SIZE"),
		Value:    20,
		Category: RollupCategory,
	}
	RPCListenAddr = &cli.StringFlag{
		Name:     "rpc.addr",
		Usage:    "RPC listening address",
		EnvVars:  prefixEnvVars("RPC_ADDR"),
		Value:    "127.0.0.1",
		Category: OperationsCategory,
	}
	RPCListenPort = &cli.IntFlag{
		Name:     "rpc.port",
		Usage:    "RPC listening port",
		EnvVars:  prefixEnvVars("RPC_PORT"),
		Value:    9545, // Note: dn-service/rpc/cli.go uses 8545 as the default.
		Category: OperationsCategory,
	}
	RPCEnableAdmin = &cli.BoolFlag{
		Name:     "rpc.enable-admin",
		Usage:    "Enable the admin API (experimental)",
		EnvVars:  prefixEnvVars("RPC_ENABLE_ADMIN"),
		Category: OperationsCategory,
	}
	RPCAdminPersistence = &cli.StringFlag{
		Name:     "rpc.admin-state",
		Usage:    "File path used to persist state changes made via the admin API so they persist across restarts. Disabled if not set.",
		EnvVars:  prefixEnvVars("RPC_ADMIN_STATE"),
		Category: OperationsCategory,
	}
	FetchWithdrawalRootFromState = &cli.BoolFlag{
		Name:     "fetch-withdrawal-root-from-state",
		Usage:    "Read withdrawal_storage_root (aka message passer storage root) from state trie (via execution layer) instead of the block header. Restores pre-Isthmus behavior, requires an archive EL client.",
		Required: false,
		EnvVars:  prefixEnvVars("FETCH_WITHDRAWAL_ROOT_FROM_STATE"),
		Category: OperationsCategory,
	}
	L1TrustRPC = &cli.BoolFlag{
		Name:     "l1.trustrpc",
		Usage:    "Trust the L1 RPC, sync faster at risk of malicious/buggy RPC providing bad or inconsistent L1 data",
		EnvVars:  prefixEnvVars("L1_TRUST_RPC"),
		Category: L1RPCCategory,
	}
	L1RPCProviderKind = &cli.GenericFlag{
		Name: "l1.rpckind",
		Usage: "The kind of RPC provider, used to inform optimal transactions receipts fetching, and thus reduce costs. Valid options: " +
			openum.EnumString(sources.RPCProviderKinds),
		EnvVars: prefixEnvVars("L1_RPC_KIND"),
		Value: func() *sources.RPCProviderKind {
			out := sources.RPCKindStandard
			return &out
		}(),
		Category: L1RPCCategory,
	}
	L2EngineKind = &cli.GenericFlag{
		Name: "l2.enginekind",
		Usage: "The kind of engine client, used to control the behavior of optimism in respect to different types of engine clients. Valid options: " +
			openum.EnumString(engine.Kinds),
		EnvVars: prefixEnvVars("L2_ENGINE_KIND"),
		Value: func() *engine.Kind {
			out := engine.Geth
			return &out
		}(),
		Category: RollupCategory,
	}
	L2EngineRpcTimeout = &cli.DurationFlag{
		Name:     "l2.engine-rpc-timeout",
		Usage:    "core engine client rpc timeout",
		EnvVars:  prefixEnvVars("L2_ENGINE_RPC_TIMEOUT"),
		Value:    time.Second * 10,
		Category: RollupCategory,
	}
	VerifierL1Confs = &cli.Uint64Flag{
		Name:     "verifier.l1-confs",
		Usage:    "Number of L1 blocks to keep distance from the L1 head before deriving core data from. Reorgs are supported, but may be slow to perform.",
		EnvVars:  prefixEnvVars("VERIFIER_L1_CONFS"),
		Value:    0,
		Category: L1RPCCategory,
	}
	SequencerEnabledFlag = &cli.BoolFlag{
		Name:     "sequencer.enabled",
		Usage:    "Enable sequencing of new core blocks. A separate batch submitter has to be deployed to publish the data for verifiers.",
		EnvVars:  prefixEnvVars("SEQUENCER_ENABLED"),
		Category: SequencerCategory,
	}
	SequencerStoppedFlag = &cli.BoolFlag{
		Name:     "sequencer.stopped",
		Usage:    "Initialize the sequencer in a stopped state. The sequencer can be started using the admin_startSequencer RPC",
		EnvVars:  prefixEnvVars("SEQUENCER_STOPPED"),
		Category: SequencerCategory,
	}
	SequencerMaxSafeLagFlag = &cli.Uint64Flag{
		Name:     "sequencer.max-safe-lag",
		Usage:    "Maximum number of core blocks for restricting the distance between core safe and unsafe. Disabled if 0.",
		EnvVars:  prefixEnvVars("SEQUENCER_MAX_SAFE_LAG"),
		Value:    0,
		Category: SequencerCategory,
	}
	SequencerL1Confs = &cli.Uint64Flag{
		Name:     "sequencer.l1-confs",
		Usage:    "Number of L1 blocks to keep distance from the L1 head as a sequencer for picking an L1 origin.",
		EnvVars:  prefixEnvVars("SEQUENCER_L1_CONFS"),
		Value:    4,
		Category: SequencerCategory,
	}
	SequencerRecoverMode = &cli.BoolFlag{
		Name:     "sequencer.recover",
		Usage:    "Forces the sequencer to strictly prepare the next L1 origin and create empty core blocks",
		EnvVars:  prefixEnvVars("SEQUENCER_RECOVER"),
		Value:    false,
		Category: SequencerCategory,
	}
	L1EpochPollIntervalFlag = &cli.DurationFlag{
		Name:     "l1.epoch-poll-interval",
		Usage:    "Poll interval for retrieving new L1 epoch updates such as safe and finalized block changes. Disabled if 0 or negative.",
		EnvVars:  prefixEnvVars("L1_EPOCH_POLL_INTERVAL"),
		Value:    time.Second * 12 * 32,
		Category: L1RPCCategory,
	}
	RuntimeConfigReloadIntervalFlag = &cli.DurationFlag{
		Name:     "l1.runtime-config-reload-interval",
		Usage:    "Poll interval for reloading the runtime config, useful when config events are not being picked up. Disabled if 0 or negative.",
		EnvVars:  prefixEnvVars("L1_RUNTIME_CONFIG_RELOAD_INTERVAL"),
		Value:    time.Minute * 10,
		Category: L1RPCCategory,
	}
	MetricsEnabledFlag = &cli.BoolFlag{
		Name:     "metrics.enabled",
		Usage:    "Enable the metrics server",
		EnvVars:  prefixEnvVars("METRICS_ENABLED"),
		Category: OperationsCategory,
	}
	MetricsAddrFlag = &cli.StringFlag{
		Name:     "metrics.addr",
		Usage:    "Metrics listening address",
		Value:    "0.0.0.0", // TODO: Switch to 127.0.0.1
		EnvVars:  prefixEnvVars("METRICS_ADDR"),
		Category: OperationsCategory,
	}
	MetricsPortFlag = &cli.IntFlag{
		Name:     "metrics.port",
		Usage:    "Metrics listening port",
		Value:    7300,
		EnvVars:  prefixEnvVars("METRICS_PORT"),
		Category: OperationsCategory,
	}
	SnapshotLog = &cli.StringFlag{
		Name:     "snapshotlog.file",
		Usage:    "Deprecated. This flag is ignored, but here for compatibility.",
		EnvVars:  prefixEnvVars("SNAPSHOT_LOG"),
		Category: OperationsCategory,
		Hidden:   true, // non-critical function, removed, flag is no-op to avoid breaking setups.
	}
	HeartbeatEnabledFlag = &cli.BoolFlag{
		Name:     "heartbeat.enabled",
		Usage:    "Deprecated, no-op flag.",
		EnvVars:  prefixEnvVars("HEARTBEAT_ENABLED"),
		Category: OperationsCategory,
		Hidden:   true,
	}
	HeartbeatMonikerFlag = &cli.StringFlag{
		Name:     "heartbeat.moniker",
		Usage:    "Deprecated, no-op flag.",
		EnvVars:  prefixEnvVars("HEARTBEAT_MONIKER"),
		Category: OperationsCategory,
		Hidden:   true,
	}
	HeartbeatURLFlag = &cli.StringFlag{
		Name:     "heartbeat.url",
		Usage:    "Deprecated, no-op flag.",
		EnvVars:  prefixEnvVars("HEARTBEAT_URL"),
		Category: OperationsCategory,
		Hidden:   true,
	}
	RollupHalt = &cli.StringFlag{
		Name:     "rollup.halt",
		Usage:    "Opt-in option to halt on incompatible protocol version requirements of the given level (major/minor/patch/none), as signaled onchain in L1",
		EnvVars:  prefixEnvVars("ROLLUP_HALT"),
		Category: RollupCategory,
	}
	RollupLoadProtocolVersions = &cli.BoolFlag{
		Name:     "rollup.load-protocol-versions",
		Usage:    "Load protocol versions from the superchain L1 ProtocolVersions contract (if available), and report in logs and metrics",
		EnvVars:  prefixEnvVars("ROLLUP_LOAD_PROTOCOL_VERSIONS"),
		Category: RollupCategory,
	}
	SafeDBPath = &cli.StringFlag{
		Name:     "safedb.path",
		Usage:    "File path used to persist safe head update data. Disabled if not set.",
		EnvVars:  prefixEnvVars("SAFEDB_PATH"),
		Category: OperationsCategory,
	}
	/* Deprecated Flags */
	L2EngineSyncEnabled = &cli.BoolFlag{
		Name:    "l2.engine-sync",
		Usage:   "WARNING: Deprecated. Use --syncmode=execution-layer instead",
		EnvVars: prefixEnvVars("L2_ENGINE_SYNC_ENABLED"),
		Value:   false,
		Hidden:  true,
	}
	SkipSyncStartCheck = &cli.BoolFlag{
		Name: "l2.skip-sync-start-check",
		Usage: "Skip sanity check of consistency of L1 origins of the unsafe core blocks when determining the sync-starting point. " +
			"This defers the L1-origin verification, and is recommended to use in when utilizing l2.engine-sync",
		EnvVars: prefixEnvVars("L2_SKIP_SYNC_START_CHECK"),
		Value:   false,
		Hidden:  true,
	}
	BetaExtraNetworks = &cli.BoolFlag{
		Name:    "beta.extra-networks",
		Usage:   "Legacy flag, ignored, all superchain-registry networks are enabled by default.",
		EnvVars: prefixEnvVars("BETA_EXTRA_NETWORKS"),
		Hidden:  true, // hidden, this is deprecated, the flag is not used anymore.
	}
	BackupL2UnsafeSyncRPC = &cli.StringFlag{
		Name:    "l2.backup-unsafe-sync-rpc",
		Usage:   "Set the backup core unsafe sync RPC endpoint.",
		EnvVars: prefixEnvVars("L2_BACKUP_UNSAFE_SYNC_RPC"),
		Hidden:  true,
	}
	BackupL2UnsafeSyncRPCTrustRPC = &cli.StringFlag{
		Name: "l2.backup-unsafe-sync-rpc.trustrpc",
		Usage: "Like l1.trustrpc, configure if response data from the RPC needs to be verified, e.g. blockhash computation." +
			"This does not include checks if the blockhash is part of the canonical chain.",
		EnvVars: prefixEnvVars("L2_BACKUP_UNSAFE_SYNC_RPC_TRUST_RPC"),
		Hidden:  true,
	}
	ConductorEnabledFlag = &cli.BoolFlag{
		Name:     "conductor.enabled",
		Usage:    "Enable the conductor service",
		EnvVars:  prefixEnvVars("CONDUCTOR_ENABLED"),
		Value:    false,
		Category: SequencerCategory,
	}
	ConductorRpcFlag = &cli.StringFlag{
		Name:     "conductor.rpc",
		Usage:    "Conductor service rpc endpoint",
		EnvVars:  prefixEnvVars("CONDUCTOR_RPC"),
		Value:    "http://127.0.0.1:8547",
		Category: SequencerCategory,
	}
	ConductorRpcTimeoutFlag = &cli.DurationFlag{
		Name:     "conductor.rpc-timeout",
		Usage:    "Conductor service rpc timeout",
		EnvVars:  prefixEnvVars("CONDUCTOR_RPC_TIMEOUT"),
		Value:    time.Second * 1,
		Category: SequencerCategory,
	}
	/* Interop flags, experimental. */
	InteropSupervisor = &cli.StringFlag{
		Name: "interop.supervisor",
		Usage: "Interop standard-mode: RPC address of interop supervisor to use for cross-chain safety verification." +
			"Applies only to Interop-enabled networks.",
		EnvVars:  prefixEnvVars("INTEROP_SUPERVISOR"),
		Category: InteropCategory,
	}
	InteropRPCAddr = &cli.StringFlag{
		Name: "interop.rpc.addr",
		Usage: "Interop Websocket-only RPC listening address, to serve supervisor syncing." +
			"Applies only to Interop-enabled networks. Optional, alternative to follow-mode.",
		EnvVars:  prefixEnvVars("INTEROP_RPC_ADDR"),
		Value:    "127.0.0.1",
		Category: InteropCategory,
	}
	InteropRPCPort = &cli.IntFlag{
		Name: "interop.rpc.port",
		Usage: "Interop RPC listening port, to serve supervisor syncing." +
			"Applies only to Interop-enabled networks.",
		EnvVars:  prefixEnvVars("INTEROP_RPC_PORT"),
		Value:    9645, // Note: dn-service/rpc/cli.go uses 8545 as the default.
		Category: InteropCategory,
	}
	InteropJWTSecret = &cli.StringFlag{
		Name: "interop.jwt-secret",
		Usage: "Interop RPC server authentication. Path to JWT secret key. Keys are 32 bytes, hex encoded in a file. " +
			"A new key will be generated if the file is empty. " +
			"Applies only to Interop-enabled networks.",
		EnvVars:     prefixEnvVars("INTEROP_JWT_SECRET"),
		Value:       "",
		Destination: new(string),
		Category:    InteropCategory,
	}

	IgnoreMissingPectraBlobSchedule = &cli.BoolFlag{
		Name: "ignore-missing-pectra-blob-schedule",
		Usage: "Ignore missing pectra blob schedule fix for Sepolia and Holesky chains. Only set if you know what you are doing!" +
			"Ask your chain's operator for the correct Pectra blob schedule activation time and set it via the rollup.json config" +
			"or use the --override.pectrablobschedule flag.",
		EnvVars:  prefixEnvVars("IGNORE_MISSING_PECTRA_BLOB_SCHEDULE"),
		Category: RollupCategory,
		Hidden:   true,
	}
	ManagerURLFlag = &cli.StringFlag{
		Name:     "pos.manager-url",
		Usage:    "WebSocket URL of the dolphinet-manager for PoS block scheduling",
		EnvVars:  prefixEnvVars("POS_MANAGER_URL"),
		Category: SequencerCategory,
	}
	PosModeEnabledFlag = &cli.BoolFlag{
		Name:     "pos.enabled",
		Usage:    "Enable the pos service",
		EnvVars:  prefixEnvVars("POS_ENABLED"),
		Value:    false,
		Category: SequencerCategory,
	}
	PoSActivationBlockFlag = &cli.Uint64Flag{
		Name:     "pos.activation-block",
		Usage:    "First block number at which PoS rules apply (Manager scheduling). 0 = PoS from genesis. Used for chain upgrade.",
		EnvVars:  prefixEnvVars("POS_ACTIVATION_BLOCK"),
		Value:    0,
		Category: SequencerCategory,
	}
	LegacySequencerFlag = &cli.BoolFlag{
		Name:     "pos.legacy-sequencer",
		Usage:    "If set, this node is the single sequencer before pos.activation-block; other nodes only sync. Set only on the node that has the pre-upgrade chain.",
		EnvVars:  prefixEnvVars("POS_LEGACY_SEQUENCER"),
		Value:    false,
		Category: SequencerCategory,
	}
	LegacySequencerAddressFlag = &cli.StringFlag{
		Name:     "pos.legacy-sequencer-address",
		Usage:    "Ethereum address of the legacy sequencer (e.g. validator1). When set on a non-legacy node, P2P accepts blocks from this address for syncing before PoS activation.",
		EnvVars:  prefixEnvVars("POS_LEGACY_SEQUENCER_ADDRESS"),
		Category: SequencerCategory,
	}
)

var requiredFlags = []cli.Flag{
	L2EngineAddr,
	L2EngineJWTSecret,
}

var optionalFlags = []cli.Flag{
	SyncModeFlag,
	FetchWithdrawalRootFromState,
	RPCListenAddr,
	RPCListenPort,
	VerifierL1Confs,
	SequencerEnabledFlag,
	SequencerStoppedFlag,
	SequencerMaxSafeLagFlag,
	SequencerL1Confs,
	SequencerRecoverMode,
	ELRpcUrlFlag,
	ELRPCRateLimit,
	ELRPCMaxBatchSize,
	L1EpochPollIntervalFlag,
	RuntimeConfigReloadIntervalFlag,
	RPCEnableAdmin,
	RPCAdminPersistence,
	MetricsEnabledFlag,
	MetricsAddrFlag,
	MetricsPortFlag,
	SnapshotLog,
	HeartbeatEnabledFlag,
	HeartbeatMonikerFlag,
	HeartbeatURLFlag,
	RollupHalt,
	RollupLoadProtocolVersions,
	ConductorEnabledFlag,
	ConductorRpcFlag,
	ConductorRpcTimeoutFlag,
	SafeDBPath,
	L2EngineKind,
	L2EngineRpcTimeout,
	InteropSupervisor,
	InteropRPCAddr,
	InteropRPCPort,
	InteropJWTSecret,
	IgnoreMissingPectraBlobSchedule,
	ManagerURLFlag,
	PosModeEnabledFlag,
	PoSActivationBlockFlag,
	LegacySequencerFlag,
	LegacySequencerAddressFlag,
}

var DeprecatedFlags = []cli.Flag{
	L2EngineSyncEnabled,
	SkipSyncStartCheck,
	BetaExtraNetworks,
	BackupL2UnsafeSyncRPC,
	BackupL2UnsafeSyncRPCTrustRPC,
	// Deprecated P2P Flags are added at the init step
}

// Flags contains the list of configuration options available to the binary.
var Flags []cli.Flag

func init() {
	DeprecatedFlags = append(DeprecatedFlags, deprecatedP2PFlags(EnvVarPrefix)...)
	optionalFlags = append(optionalFlags, P2PFlags(EnvVarPrefix)...)
	optionalFlags = append(optionalFlags, oplog.CLIFlagsWithCategory(EnvVarPrefix, OperationsCategory)...)
	optionalFlags = append(optionalFlags, oppprof.CLIFlagsWithCategory(EnvVarPrefix, OperationsCategory)...)
	optionalFlags = append(optionalFlags, DeprecatedFlags...)
	optionalFlags = append(optionalFlags, opflags.CLIFlags(EnvVarPrefix, RollupCategory)...)
	Flags = append(requiredFlags, optionalFlags...)
}

func CheckRequired(ctx *cli.Context) error {
	for _, f := range requiredFlags {
		if !ctx.IsSet(f.Names()[0]) {
			return fmt.Errorf("flag %s is required", f.Names()[0])
		}
	}
	return opflags.CheckRequiredXor(ctx)
}
