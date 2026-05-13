package opnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/chaincfg"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/flags"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/node"
	p2pcli "github.com/dolphinnet-labs/dolphinnet/dn-node/p2p/cli"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/driver"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/engine"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/interop"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/sync"
	opflags "github.com/dolphinnet-labs/dolphinnet/dn-service/flags"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/oppprof"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/rpc"
)

// NewConfig creates a Config from the provided flags or environment variables.
func NewConfig(ctx *cli.Context, log log.Logger) (*node.Config, error) {
	if err := flags.CheckRequired(ctx); err != nil {
		return nil, err
	}

	rollupConfig, err := NewRollupConfigFromCLI(log, ctx)
	if err != nil {
		return nil, err
	}

	configPersistence := NewConfigPersistence(ctx)

	driverConfig := NewDriverConfig(ctx)

	p2pSignerSetup, p2pSignerAddress, err := p2pcli.LoadSignerSetup(ctx, log)
	if err != nil {
		return nil, fmt.Errorf("failed to load p2p signer: %w", err)
	}

	p2pConfig, err := p2pcli.NewConfig(ctx, rollupConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load p2p config: %w", err)
	}

	l2Endpoint, err := NewL2EndpointConfig(ctx, log)
	if err != nil {
		return nil, fmt.Errorf("failed to load l2 endpoints info: %w", err)
	}

	elEndpoint, err := NewElEndpointConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load l2 endpoints info: %w", err)
	}

	syncConfig, err := NewSyncConfig(ctx, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create the sync config: %w", err)
	}

	haltOption := ctx.String(flags.RollupHalt.Name)
	if haltOption == "none" {
		haltOption = ""
	}

	poSActivationBlock, err := strconv.ParseUint(ctx.String(flags.PoSActivationBlockFlag.Name), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parseUint the pos activation block: %w", err)
	}

	if ctx.IsSet(flags.HeartbeatEnabledFlag.Name) ||
		ctx.IsSet(flags.HeartbeatMonikerFlag.Name) ||
		ctx.IsSet(flags.HeartbeatURLFlag.Name) {
		log.Warn("Heartbeat functionality is not supported anymore, CLI flags will be removed in following release.")
	}
	conductorRPCEndpoint := ctx.String(flags.ConductorRpcFlag.Name)
	cfg := &node.Config{
		L2:            l2Endpoint,
		El:            elEndpoint,
		Rollup:        *rollupConfig,
		Driver:        *driverConfig,
		InteropConfig: NewSupervisorEndpointConfig(ctx),
		RPC: node.RPCConfig{
			ListenAddr:  ctx.String(flags.RPCListenAddr.Name),
			ListenPort:  ctx.Int(flags.RPCListenPort.Name),
			EnableAdmin: ctx.Bool(flags.RPCEnableAdmin.Name),
		},
		Metrics: node.MetricsConfig{
			Enabled:    ctx.Bool(flags.MetricsEnabledFlag.Name),
			ListenAddr: ctx.String(flags.MetricsAddrFlag.Name),
			ListenPort: ctx.Int(flags.MetricsPortFlag.Name),
		},
		Pprof:                       oppprof.ReadCLIConfig(ctx),
		P2P:                         p2pConfig,
		P2PSigner:                   p2pSignerSetup,
		P2PSignerAddress:            p2pSignerAddress,
		L1EpochPollInterval:         ctx.Duration(flags.L1EpochPollIntervalFlag.Name),
		RuntimeConfigReloadInterval: ctx.Duration(flags.RuntimeConfigReloadIntervalFlag.Name),
		ConfigPersistence:           configPersistence,
		SafeDBPath:                  ctx.String(flags.SafeDBPath.Name),
		Sync:                        *syncConfig,
		RollupHalt:                  haltOption,

		ConductorEnabled: ctx.Bool(flags.ConductorEnabledFlag.Name),
		ConductorRpc: func(context.Context) (string, error) {
			return conductorRPCEndpoint, nil
		},
		ConductorRpcTimeout: ctx.Duration(flags.ConductorRpcTimeoutFlag.Name),

		IgnoreMissingPectraBlobSchedule: ctx.Bool(flags.IgnoreMissingPectraBlobSchedule.Name),
		FetchWithdrawalRootFromState:    ctx.Bool(flags.FetchWithdrawalRootFromState.Name),

		ManagerURL:             ctx.String(flags.ManagerURLFlag.Name),
		PoSActivationBlock:     poSActivationBlock,
		LegacySequencerAddress: common.HexToAddress(ctx.String(flags.LegacySequencerAddressFlag.Name)),
	}

	if err := cfg.LoadPersisted(log); err != nil {
		return nil, fmt.Errorf("failed to load driver config: %w", err)
	}
	cfg.Driver.PoSActivationBlock = cfg.PoSActivationBlock

	// conductor controls the sequencer state
	if cfg.ConductorEnabled {
		cfg.Driver.SequencerStopped = true
	}

	if err := cfg.Check(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func NewSupervisorEndpointConfig(ctx *cli.Context) *interop.Config {
	return &interop.Config{
		SupervisorAddr:   ctx.String(flags.InteropSupervisor.Name),
		RPCAddr:          ctx.String(flags.InteropRPCAddr.Name),
		RPCPort:          ctx.Int(flags.InteropRPCPort.Name),
		RPCJwtSecretPath: ctx.String(flags.InteropJWTSecret.Name),
	}
}

func NewL2EndpointConfig(ctx *cli.Context, logger log.Logger) (*node.L2EndpointConfig, error) {
	l2Addr := ctx.String(flags.L2EngineAddr.Name)
	fileName := ctx.String(flags.L2EngineJWTSecret.Name)
	secret, err := rpc.ObtainJWTSecret(logger, fileName, true)
	if err != nil {
		return nil, err
	}
	l2RpcTimeout := ctx.Duration(flags.L2EngineRpcTimeout.Name)
	return &node.L2EndpointConfig{
		L2EngineAddr:        l2Addr,
		L2EngineJWTSecret:   secret,
		L2EngineCallTimeout: l2RpcTimeout,
	}, nil
}

func NewElEndpointConfig(ctx *cli.Context) (*node.ElEndpointConfig, error) {
	return &node.ElEndpointConfig{
		ElRpcAddr: ctx.String(flags.ELRpcUrlFlag.Name),
		RateLimit: ctx.Float64(flags.ELRPCRateLimit.Name),
		BatchSize: ctx.Int(flags.ELRPCMaxBatchSize.Name),
	}, nil
}

func NewConfigPersistence(ctx *cli.Context) node.ConfigPersistence {
	stateFile := ctx.String(flags.RPCAdminPersistence.Name)
	if stateFile == "" {
		return node.DisabledConfigPersistence{}
	}
	return node.NewConfigPersistence(stateFile)
}

func NewDriverConfig(ctx *cli.Context) *driver.Config {
	return &driver.Config{
		VerifierConfDepth:   ctx.Uint64(flags.VerifierL1Confs.Name),
		SequencerConfDepth:  ctx.Uint64(flags.SequencerL1Confs.Name),
		SequencerEnabled:    ctx.Bool(flags.SequencerEnabledFlag.Name),
		SequencerStopped:    ctx.Bool(flags.SequencerStoppedFlag.Name),
		SequencerMaxSafeLag: ctx.Uint64(flags.SequencerMaxSafeLagFlag.Name),
		RecoverMode:         ctx.Bool(flags.SequencerRecoverMode.Name),
		PosMode:             ctx.Bool(flags.PosModeEnabledFlag.Name),
		LegacySequencer:     ctx.Bool(flags.LegacySequencerFlag.Name),
		EngineCallTimeout:   ctx.Duration(flags.L2EngineRpcTimeout.Name),
	}
}

func NewRollupConfigFromCLI(log log.Logger, ctx *cli.Context) (*rollup.Config, error) {
	network := ctx.String(opflags.NetworkFlagName)
	rollupConfigPath := ctx.String(opflags.RollupConfigFlagName)
	if ctx.Bool(flags.BetaExtraNetworks.Name) {
		log.Warn("The beta.extra-networks flag is deprecated and can be omitted safely.")
	}
	rollupConfig, err := NewRollupConfig(log, network, rollupConfigPath)
	if err != nil {
		return nil, err
	}
	applyOverrides(ctx, rollupConfig)
	return rollupConfig, nil
}

func NewRollupConfig(log log.Logger, network string, rollupConfigPath string) (*rollup.Config, error) {
	if network != "" {
		if rollupConfigPath != "" {
			log.Error(`Cannot configure network and rollup-config at the same time.
Startup will proceed to use the network-parameter and ignore the rollup config.
Conflicting configuration is deprecated, and will stop the dn-node from starting in the future.
`, "network", network, "rollup_config", rollupConfigPath)
		}
		rollupConfig, err := chaincfg.GetRollupConfig(network)
		if err != nil {
			return nil, err
		}
		return rollupConfig, nil
	}

	file, err := os.Open(rollupConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read rollup config: %w", err)
	}
	defer file.Close()

	var rollupConfig rollup.Config
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rollupConfig); err != nil {
		return nil, fmt.Errorf("failed to decode rollup config: %w", err)
	}
	return &rollupConfig, nil
}

func applyOverrides(ctx *cli.Context, rollupConfig *rollup.Config) {
	if ctx.IsSet(opflags.CanyonOverrideFlagName) {
		canyon := ctx.Uint64(opflags.CanyonOverrideFlagName)
		rollupConfig.CanyonTime = &canyon
	}
	if ctx.IsSet(opflags.DeltaOverrideFlagName) {
		delta := ctx.Uint64(opflags.DeltaOverrideFlagName)
		rollupConfig.DeltaTime = &delta
	}
	if ctx.IsSet(opflags.EcotoneOverrideFlagName) {
		ecotone := ctx.Uint64(opflags.EcotoneOverrideFlagName)
		rollupConfig.EcotoneTime = &ecotone
	}
	if ctx.IsSet(opflags.FjordOverrideFlagName) {
		fjord := ctx.Uint64(opflags.FjordOverrideFlagName)
		rollupConfig.FjordTime = &fjord
	}
	if ctx.IsSet(opflags.GraniteOverrideFlagName) {
		granite := ctx.Uint64(opflags.GraniteOverrideFlagName)
		rollupConfig.GraniteTime = &granite
	}
	if ctx.IsSet(opflags.HoloceneOverrideFlagName) {
		holocene := ctx.Uint64(opflags.HoloceneOverrideFlagName)
		rollupConfig.HoloceneTime = &holocene
	}
	if ctx.IsSet(opflags.PectraBlobScheduleOverrideFlagName) {
		pectrablobschedule := ctx.Uint64(opflags.PectraBlobScheduleOverrideFlagName)
		rollupConfig.PectraBlobScheduleTime = &pectrablobschedule
	}
	if ctx.IsSet(opflags.IsthmusOverrideFlagName) {
		isthmus := ctx.Uint64(opflags.IsthmusOverrideFlagName)
		rollupConfig.IsthmusTime = &isthmus
	}
	if ctx.IsSet(opflags.InteropOverrideFlagName) {
		interop := ctx.Uint64(opflags.InteropOverrideFlagName)
		rollupConfig.InteropTime = &interop
	}
}

func NewSyncConfig(ctx *cli.Context, log log.Logger) (*sync.Config, error) {
	if ctx.IsSet(flags.L2EngineSyncEnabled.Name) && ctx.IsSet(flags.SyncModeFlag.Name) {
		return nil, errors.New("cannot set both --l2.engine-sync and --syncmode at the same time")
	} else if ctx.IsSet(flags.L2EngineSyncEnabled.Name) {
		log.Error("l2.engine-sync is deprecated and will be removed in a future release. Use --syncmode=execution-layer instead.")
	}
	mode, err := sync.StringToMode(ctx.String(flags.SyncModeFlag.Name))
	if err != nil {
		return nil, err
	}

	engineKind := engine.Kind(ctx.String(flags.L2EngineKind.Name))
	cfg := &sync.Config{
		SyncMode:                       mode,
		SkipSyncStartCheck:             ctx.Bool(flags.SkipSyncStartCheck.Name),
		SupportsPostFinalizationELSync: engineKind.SupportsPostFinalizationELSync(),
		FindHeadsTimeout:               ctx.Duration(flags.FindHeadsTimeoutFlag.Name),
	}
	if ctx.Bool(flags.L2EngineSyncEnabled.Name) {
		cfg.SyncMode = sync.ELSync
	}

	return cfg, nil
}
