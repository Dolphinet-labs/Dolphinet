package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/metrics"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/node/safedb"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/p2p"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/pos"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/driver"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/event"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/interop"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/interop/managed"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/sequencing"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/sync"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/eth"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/httputil"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/oppprof"
	oprpc "github.com/dolphinnet-labs/dolphinnet/dn-service/rpc"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/sources"
)

var ErrAlreadyClosed = errors.New("node is already closed")

type closableSafeDB interface {
	rollup.SafeHeadListener
	SafeDBReader
	io.Closer
}

type OpNode struct {
	// Retain the config to test for active features rather than test for runtime state.
	cfg        *Config
	log        log.Logger
	appVersion string
	metrics    *metrics.Metrics

	l1HeadsSub     ethereum.Subscription // Subscription to get L1 heads (automatically re-subscribes on error)
	l1SafeSub      ethereum.Subscription // Subscription to get L1 safe blocks, a.k.a. justified data (polling)
	l1FinalizedSub ethereum.Subscription // Subscription to get L1 safe blocks, a.k.a. justified data (polling)

	eventSys   event.System
	eventDrain event.Drainer

	l2Driver  *driver.Driver        // core Engine to Sync
	l2Source  *sources.EngineClient // core Execution Engine RPC bindings
	elClient  *sources.EthClient    // for the replica node synchronizes the specified block information
	server    *oprpc.Server         // RPC server hosting the rollup-node API
	p2pNode   *p2p.NodeP2P          // P2P node functionality
	p2pMu     gosync.Mutex          // protects p2pNode
	p2pSigner p2p.Signer            // p2p gossip application messages will be signed with this signer
	tracer    Tracer                // tracer to get events for testing/debugging
	runCfg    *RuntimeConfig        // runtime configurables

	safeDB closableSafeDB

	rollupHalt string // when to halt the rollup, disabled if empty

	pprofService *oppprof.Service
	metricsSrv   *httputil.HTTPServer

	beacon *sources.L1BeaconClient

	interopSys interop.SubSystem

	// PoS Manager client
	managerClient *pos.ManagerClient
	posEmitter    event.Emitter // emitter for triggering SequencerActionEvent

	// Current epoch schedule for determining block producer
	currentEpochSchedule *pos.EpochSchedule
	// Historical epoch schedules (max 5) for calculating safe and finalized blocks
	epochScheduleHistory []*pos.EpochSchedule
	epochScheduleMu      gosync.RWMutex

	// some resources cannot be stopped directly, like the p2p gossipsub router (not our design),
	// and depend on this ctx to be closed.
	resourcesCtx   context.Context
	resourcesClose context.CancelFunc

	// Indicates when it's safe to close data sources used by the runtimeConfig bg loader
	runtimeConfigReloaderDone chan struct{}

	closed atomic.Bool

	// cancels execution prematurely, e.g. to halt. This may be nil.
	cancel context.CancelCauseFunc
	halted atomic.Bool
}

// The OpNode handles incoming gossip
var _ p2p.GossipIn = (*OpNode)(nil)

// New creates a new OpNode instance.
// The provided ctx argument is for the span of initialization only;
// the node will immediately Stop(ctx) before finishing initialization if the context is canceled during initialization.
func New(ctx context.Context, cfg *Config, log log.Logger, appVersion string, m *metrics.Metrics) (*OpNode, error) {
	if err := cfg.Check(); err != nil {
		return nil, err
	}

	n := &OpNode{
		cfg:        cfg,
		log:        log,
		appVersion: appVersion,
		metrics:    m,
		rollupHalt: cfg.RollupHalt,
		cancel:     cfg.Cancel,
	}
	// not a context leak, gossipsub is closed with a context.
	n.resourcesCtx, n.resourcesClose = context.WithCancel(context.Background())

	err := n.init(ctx, cfg)
	if err != nil {
		log.Error("Error initializing the rollup node", "err", err)
		// ensure we always close the node resources if we fail to initialize the node.
		if closeErr := n.Stop(ctx); closeErr != nil {
			return nil, multierror.Append(err, closeErr)
		}
		return nil, err
	}
	return n, nil
}

func (n *OpNode) init(ctx context.Context, cfg *Config) error {
	n.log.Info("Initializing rollup node", "version", n.appVersion)
	if err := n.initTracer(ctx, cfg); err != nil {
		return fmt.Errorf("failed to init the trace: %w", err)
	}
	n.initEventSystem()
	if cfg.Sync.SyncMode == sync.ELSync {
		if err := n.initElClient(ctx, cfg); err != nil {
			return fmt.Errorf("failed to init core: %w", err)
		}
	}
	if err := n.initL2(ctx, cfg); err != nil {
		return fmt.Errorf("failed to init core: %w", err)
	}
	if err := n.initRuntimeConfig(ctx, cfg); err != nil { // depends on core, to signal initial runtime values to
		return fmt.Errorf("failed to init the runtime config: %w", err)
	}
	if err := n.initP2PSigner(ctx, cfg); err != nil {
		return fmt.Errorf("failed to init the P2P signer: %w", err)
	}
	if err := n.initP2P(cfg); err != nil {
		return fmt.Errorf("failed to init the P2P stack: %w", err)
	}
	// Only expose the server at the end, ensuring all RPC backend components are initialized.
	if err := n.initRPCServer(cfg); err != nil {
		return fmt.Errorf("failed to init the RPC server: %w", err)
	}
	if err := n.initMetricsServer(cfg); err != nil {
		return fmt.Errorf("failed to init the metrics server: %w", err)
	}
	n.metrics.RecordInfo(n.appVersion)
	n.metrics.RecordUp()
	if err := n.initPProf(cfg); err != nil {
		return fmt.Errorf("failed to init profiling: %w", err)
	}
	if err := n.initManagerClient(ctx, cfg); err != nil {
		return fmt.Errorf("failed to init manager client: %w", err)
	}
	return nil
}

func (n *OpNode) initEventSystem() {
	// This executor will be configurable in the future, for parallel event processing
	executor := event.NewGlobalSynchronous(n.resourcesCtx)
	sys := event.NewSystem(n.log, executor)
	sys.AddTracer(event.NewMetricsTracer(n.metrics))
	sys.Register("node", event.DeriverFunc(n.onEvent), event.DefaultRegisterOpts())
	n.eventSys = sys
	n.eventDrain = executor
}

func (n *OpNode) initTracer(ctx context.Context, cfg *Config) error {
	if cfg.Tracer != nil {
		n.tracer = cfg.Tracer
	} else {
		n.tracer = new(noOpTracer)
	}
	return nil
}

func (n *OpNode) initRuntimeConfig(ctx context.Context, cfg *Config) error {
	// attempt to load runtime config, repeat N times
	n.runCfg = NewRuntimeConfig(n.log, &cfg.Rollup)

	fetchCtx, fetchCancel := context.WithTimeout(ctx, time.Second*10)
	err := n.runCfg.Load(fetchCtx, cfg.P2PSignerAddress)
	fetchCancel()
	if err != nil {
		n.log.Error("failed to fetch runtime config data", "err", err)
		return err
	}
	if cfg.LegacySequencerAddress != (common.Address{}) {
		n.runCfg.SetLegacySequencerAddress(cfg.LegacySequencerAddress)
	}
	return nil
}

func (n *OpNode) initL2(ctx context.Context, cfg *Config) error {
	rpcClient, rpcCfg, err := cfg.L2.Setup(ctx, n.log, &cfg.Rollup, n.metrics)
	if err != nil {
		return fmt.Errorf("failed to setup core execution-engine RPC client: %w", err)
	}

	rpcCfg.FetchWithdrawalRootFromState = cfg.FetchWithdrawalRootFromState

	n.l2Source, err = sources.NewEngineClient(rpcClient, n.log, n.metrics.L2SourceCache, rpcCfg)
	if err != nil {
		return fmt.Errorf("failed to create Engine client: %w", err)
	}

	if err := cfg.Rollup.ValidateL2Config(ctx, n.l2Source, cfg.Sync.SyncMode == sync.ELSync); err != nil {
		return err
	}

	managedMode := false
	if cfg.Rollup.InteropTime != nil {
		sys, err := cfg.InteropConfig.Setup(ctx, n.log, &n.cfg.Rollup, n.l2Source, n.metrics)
		if err != nil {
			return fmt.Errorf("failed to setup interop: %w", err)
		}
		if _, ok := sys.(*managed.ManagedMode); ok {
			managedMode = ok
		}
		n.interopSys = sys
		n.eventSys.Register("interop", n.interopSys, event.DefaultRegisterOpts())
	}

	if cfg.SafeDBPath != "" {
		n.log.Info("Safe head database enabled", "path", cfg.SafeDBPath)
		safeDB, err := safedb.NewSafeDB(n.log, cfg.SafeDBPath)
		if err != nil {
			return fmt.Errorf("failed to create safe head database at %v: %w", cfg.SafeDBPath, err)
		}
		n.safeDB = safeDB
	} else {
		n.safeDB = safedb.Disabled
	}

	if cfg.Rollup.ChainOpConfig == nil {
		return fmt.Errorf("cfg.Rollup.ChainOpConfig is nil. Please see https://github.com/dolphinnet-labs/dolphinnet/releases/tag/dn-node/v1.11.0: %w", err)
	}

	var blockProducerChecker sequencing.BlockProducerChecker
	if cfg.Driver.PosMode {
		blockProducerChecker = n
	}
	n.l2Driver = driver.NewDriver(n.eventSys, n.eventDrain, &cfg.Driver, &cfg.Rollup, n.l2Source, n.elClient, n, n, n.log, n.metrics, cfg.ConfigPersistence, n.safeDB, &cfg.Sync, managedMode, cfg.Driver.MaxRequestsPerBatch, blockProducerChecker)

	n.l2Driver.SetEpochInfoGetter(n)

	if cfg.ManagerURL != "" {
		n.posEmitter = n.eventSys.Register("pos-manager", nil, event.DefaultRegisterOpts())
	}

	return nil
}

func (n *OpNode) initElClient(ctx context.Context, cfg *Config) error {
	elRpcClient, rpcCfg, err := cfg.El.Setup(ctx, n.log, &cfg.Rollup)
	if err != nil {
		return fmt.Errorf("failed to setup core execution-layer RPC client: %w", err)
	}
	n.elClient, err = sources.NewEthClient(elRpcClient, n.log, n.metrics.L2SourceCache, rpcCfg)
	if err != nil {
		return fmt.Errorf("failed to create execution-layer client: %w", err)
	}
	cfg.Driver.MaxRequestsPerBatch = rpcCfg.MaxRequestsPerBatch
	return nil
}

func (n *OpNode) initRPCServer(cfg *Config) error {
	server := newRPCServer(&cfg.RPC, &cfg.Rollup,
		n.l2Source.L2Client, n.l2Driver, n.safeDB,
		n.log, n.metrics, n.appVersion)
	if p2pNode := n.getP2PNodeIfEnabled(); p2pNode != nil {
		server.AddAPI(rpc.API{
			Namespace: p2p.NamespaceRPC,
			Service:   p2p.NewP2PAPIBackend(p2pNode, n.log),
		})
		n.log.Info("P2P RPC enabled")
	}
	if cfg.RPC.EnableAdmin {
		server.AddAPI(rpc.API{
			Namespace: "admin",
			Service:   NewAdminAPI(n.l2Driver, n.log),
		})
		n.log.Info("Admin RPC enabled")
	}
	server.AddAPI(rpc.API{
		Namespace: "opnode",
		Service:   NewOpNodeAPI(n),
	})
	n.log.Info("OpNode RPC enabled")
	n.log.Info("Starting JSON-RPC server")
	if err := server.Start(); err != nil {
		return fmt.Errorf("unable to start RPC server: %w", err)
	}
	n.log.Info("Started JSON-RPC server", "addr", server.Endpoint())
	n.server = server
	return nil
}

func (n *OpNode) initMetricsServer(cfg *Config) error {
	if !cfg.Metrics.Enabled {
		n.log.Info("metrics disabled")
		return nil
	}
	n.log.Debug("starting metrics server", "addr", cfg.Metrics.ListenAddr, "port", cfg.Metrics.ListenPort)
	metricsSrv, err := n.metrics.StartServer(cfg.Metrics.ListenAddr, cfg.Metrics.ListenPort)
	if err != nil {
		return fmt.Errorf("failed to start metrics server: %w", err)
	}
	n.log.Info("started metrics server", "addr", metricsSrv.Addr())
	n.metricsSrv = metricsSrv
	return nil
}

func (n *OpNode) initPProf(cfg *Config) error {
	n.pprofService = oppprof.New(
		cfg.Pprof.ListenEnabled,
		cfg.Pprof.ListenAddr,
		cfg.Pprof.ListenPort,
		cfg.Pprof.ProfileType,
		cfg.Pprof.ProfileDir,
		cfg.Pprof.ProfileFilename,
	)

	if err := n.pprofService.Start(); err != nil {
		return fmt.Errorf("failed to start pprof service: %w", err)
	}

	return nil
}

func (n *OpNode) p2pEnabled() bool {
	return n.cfg.P2PEnabled()
}

func (n *OpNode) initP2P(cfg *Config) (err error) {
	n.p2pMu.Lock()
	defer n.p2pMu.Unlock()
	if n.p2pNode != nil {
		panic("p2p node already initialized")
	}
	if n.p2pEnabled() {
		// TODO(protocol-quest#97): Use EL Sync instead of CL Alt sync for fetching missing blocks in the payload queue.
		l2ChainForP2P := &p2pL2ChainAdapter{EngineClient: n.l2Source}
		n.p2pNode, err = p2p.NewNodeP2P(n.resourcesCtx, &cfg.Rollup, n.log, cfg.P2P, n, l2ChainForP2P, n.runCfg, n.metrics, false)
		if err != nil {
			return
		}
		if n.p2pNode.Dv5Udp() != nil {
			go n.p2pNode.DiscoveryProcess(n.resourcesCtx, n.log, &cfg.Rollup, cfg.P2P.TargetPeers())
		}
	}
	return nil
}

func (n *OpNode) initP2PSigner(ctx context.Context, cfg *Config) (err error) {
	// the p2p signer setup is optional
	if cfg.P2PSigner == nil {
		return
	}
	// p2pSigner may still be nil, the signer setup may not create any signer, the signer is optional
	n.p2pSigner, err = cfg.P2PSigner.SetupSigner(ctx)
	return
}

func (n *OpNode) initManagerClient(ctx context.Context, cfg *Config) error {
	if cfg.ManagerURL == "" {
		n.log.Info("PoS manager not configured, using default sequencer mode")
		return nil
	}

	n.log.Info("Initializing PoS manager client",
		"manager_url", cfg.ManagerURL,
		"validator_address", cfg.P2PSignerAddress.Hex())

	// Use P2PSignerAddress as validator address
	client := pos.NewManagerClient(cfg.ManagerURL, cfg.P2PSignerAddress, n.log)

	client.SetBootnodesProvider(func() []string {
		p2pNode := n.getP2PNodeIfEnabled()
		if p2pNode == nil || p2pNode.Dv5Udp() == nil {
			return nil
		}
		self := p2pNode.Dv5Udp().Self()
		if self == nil {
			return nil
		}
		return []string{self.String()}
	})

	client.SetOnBootnodesUpdate(func(bootnodes []string) error {
		p2pNode := n.getP2PNodeIfEnabled()
		if p2pNode == nil {
			return nil
		}
		added, err := p2pNode.AddDiscv5Bootnodes(bootnodes)
		if err != nil {
			n.log.Warn("Failed to add discv5 bootnodes from manager update", "err", err)
		}
		if added > 0 {
			n.log.Info("Imported bootnodes from manager update", "added", added, "count", len(bootnodes))
		}
		return nil
	})

	// Set block assignment callback
	// Manager only sends to validators that should produce blocks based on scheduler, so receiving a message means we should produce a block
	// After block production, it will automatically broadcast to other validator nodes via P2P (implemented in PublishL2Payload)
	// Other validators receive P2P messages via OnUnsafeL2Payload to complete synchronization
	client.SetOnBlockAssignment(func(assignment pos.BlockAssignment) error {
		if cfg.PoSActivationBlock != 0 && assignment.BlockNumber < cfg.PoSActivationBlock {
			n.log.Debug("Ignoring block assignment before PoS activation", "block_number", assignment.BlockNumber, "pos_activation_block", cfg.PoSActivationBlock)
			return nil
		}
		n.log.Info("Received block assignment from manager",
			"block_number", assignment.BlockNumber,
			"epoch", assignment.Epoch,
			"validator", assignment.Validator.Hex())

		// Notify manager that block production has started
		if err := client.SendValidatorStatus(assignment.BlockNumber, "mining"); err != nil {
			n.log.Warn("Failed to send validator status", "err", err)
		}

		// Trigger sequencer to produce block
		// After block production, sequencer will automatically trigger PublishL2Payload via asyncGossip for P2P broadcast
		if n.posEmitter != nil {
			n.posEmitter.Emit(sequencing.SequencerActionEvent{})
		}

		return nil
	})

	// Set heartbeat callback to detect if manager is alive
	client.SetOnHeartbeat(func(heartbeat pos.HeartbeatMessage) error {
		n.log.Debug("Received heartbeat from manager",
			"block_number", heartbeat.BlockNumber,
			"epoch", heartbeat.Epoch)
		return nil
	})

	// Set epoch schedule callback
	client.SetOnEpochSchedule(func(schedule pos.EpochSchedule) error {
		n.log.Info("Received epoch schedule from manager",
			"epoch", schedule.Epoch,
			"start_block", schedule.StartBlock,
			"end_block", schedule.EndBlock)

		// Store current epoch schedule for determining block producer
		n.epochScheduleMu.Lock()
		if n.currentEpochSchedule != nil {
			found := false
			for _, hist := range n.epochScheduleHistory {
				if hist.Epoch == n.currentEpochSchedule.Epoch {
					found = true
					break
				}
			}
			if !found {
				n.epochScheduleHistory = append(n.epochScheduleHistory, n.currentEpochSchedule)
				const maxHistorySize = 5
				if len(n.epochScheduleHistory) > maxHistorySize {
					n.epochScheduleHistory = n.epochScheduleHistory[1:]
				}
			}
		}
		n.currentEpochSchedule = &schedule
		n.epochScheduleMu.Unlock()

		if len(schedule.Validators) > 0 && (cfg.PoSActivationBlock == 0 || schedule.StartBlock >= cfg.PoSActivationBlock) {
			validatorAddrs := make([]common.Address, 0, len(schedule.Validators))
			for addr := range schedule.Validators {
				validatorAddrs = append(validatorAddrs, addr)
			}
			n.runCfg.UpdateAllowedSequencerAddresses(validatorAddrs)
			n.log.Info("Updated allowed sequencer addresses from epoch schedule",
				"count", len(validatorAddrs))
		}

		return nil
	})

	client.SetOnVoteReward(func(reward pos.VoteRewardMessage) error {
		n.log.Info("Received vote reward from manager",
			"target_block", reward.TargetBlock,
			"voted_block", reward.VotedBlock,
			"reward_count", len(reward.Rewards))

		if n.l2Source == nil || n.l2Source.EngineAPIClient == nil {
			n.log.Warn("L2Source not available, cannot set vote rewards")
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		rewards := make([]map[string]interface{}, 0, len(reward.Rewards))
		for _, r := range reward.Rewards {
			rewards = append(rewards, map[string]interface{}{
				"voter":  r.Voter.Hex(),
				"amount": r.Amount,
			})
		}

		err := n.l2Source.EngineAPIClient.RPC.CallContext(ctx, nil, "engine_setVoteRewards", hexutil.Uint64(reward.TargetBlock), rewards)
		if err != nil {
			n.log.Error("Failed to set vote rewards in geth", "err", err, "target_block", reward.TargetBlock, "voted_block", reward.VotedBlock)
			return err
		}

		n.log.Info("Set vote rewards in geth", "target_block", reward.TargetBlock, "voted_block", reward.VotedBlock, "count", len(rewards))
		return nil
	})

	client.SetOnVoteRequest(func(request pos.VoteRequestMessage) error {
		n.log.Info("Received vote request from manager",
			"block_number", request.BlockNumber,
			"deadline", request.VoteDeadline)

		if time.Now().After(request.VoteDeadline) {
			n.log.Warn("Vote deadline has passed, ignoring vote request", "block", request.BlockNumber)
			return nil
		}

		if n.l2Source == nil || n.l2Source.L2Client == nil {
			n.log.Warn("L2Source not available, cannot fetch block hash")
			return nil
		}

		var blockRef eth.L2BlockRef
		var err error
		retryInterval := 500 * time.Millisecond
		maxRetries := int(time.Until(request.VoteDeadline) / retryInterval)
		if maxRetries > 20 {
			maxRetries = 20
		}
		if maxRetries < 1 {
			maxRetries = 1
		}

		for attempt := 0; attempt < maxRetries; attempt++ {
			if time.Now().After(request.VoteDeadline) {
				n.log.Warn("Vote deadline passed while retrying", "block", request.BlockNumber, "attempts", attempt+1)
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			blockRef, err = n.l2Source.L2Client.L2BlockRefByNumber(ctx, request.BlockNumber)
			cancel()

			if err == nil {
				break
			}

			if attempt < maxRetries-1 {
				time.Sleep(retryInterval)
			}
		}

		if err != nil {
			n.log.Error("Failed to get block ref for voting after retries", "block", request.BlockNumber, "err", err, "attempts", maxRetries)
			return err
		}

		blockHash := blockRef.Hash

		voteMsg := pos.BlockVoteMessage{
			Type:        pos.MessageTypeBlockVote,
			Voter:       n.cfg.P2PSignerAddress,
			BlockNumber: request.BlockNumber,
			BlockHash:   blockHash,
			Timestamp:   time.Now(),
		}

		if err := client.SendBlockVote(voteMsg); err != nil {
			n.log.Error("Failed to send block vote", "err", err)
			return err
		}

		n.log.Info("Sent block vote", "block", request.BlockNumber, "hash", blockHash.Hex())
		return nil
	})

	client.SetOnRegisterAck(func(status string) error {
		if status != "success" {
			n.log.Warn("Registration failed, skipping vote reward request", "status", status)
			return nil
		}

		n.log.Info("Registration successful, requesting missing vote rewards")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		head, err := n.l2Source.L2BlockRefByLabel(ctx, eth.Unsafe)
		if err != nil {
			n.log.Warn("Failed to get local head for vote reward request", "err", err)
			return nil
		}

		startBlock := head.Number

		n.log.Info("Requesting all vote rewards from manager",
			"start_block", startBlock,
			"local_head", head.Number)

		if err := client.RequestVoteRewards(startBlock); err != nil {
			n.log.Error("Failed to request vote rewards from manager", "err", err)
			return err
		}

		return nil
	})

	n.managerClient = client

	return nil
}

func (n *OpNode) Start(ctx context.Context) error {
	if n.interopSys != nil {
		if err := n.interopSys.Start(ctx); err != nil {
			n.log.Error("Could not start interop sub system", "err", err)
			return err
		}
	}

	// Start manager client (if configured)
	if n.managerClient != nil {
		if err := n.managerClient.Start(ctx); err != nil {
			n.log.Error("Could not start manager client", "err", err)
			return err
		}
		n.log.Info("Manager client started")
	}

	n.log.Info("Starting execution engine driver")
	// start driving engine: sync blocks by deriving them from L1 and driving them into the engine
	if err := n.l2Driver.Start(); err != nil {
		n.log.Error("Could not start a rollup node", "err", err)
		return err
	}
	log.Info("Rollup node started")

	if n.managerClient != nil {
		go n.posRegisterLoop(ctx)
	}
	return nil
}

func (n *OpNode) posRegisterLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n.managerClient == nil || !n.managerClient.IsConnected() {
				continue
			}
			status, err := n.l2Driver.SyncStatus(ctx)
			if err != nil || status == nil {
				continue
			}
			nextBlock := status.UnsafeL2.Number + 1
			if n.cfg.PoSActivationBlock != 0 && nextBlock < n.cfg.PoSActivationBlock {
				n.log.Debug("PoS registration deferred until activation height", "next_block", nextBlock, "pos_activation_block", n.cfg.PoSActivationBlock)
				continue
			}
			if err := n.managerClient.RegisterWithBlockNumber(nextBlock); err != nil {
				n.log.Warn("Failed to register with manager", "err", err)
			}
		}
	}
}

// onEvent handles broadcast events.
// The OpNode itself is a deriver to catch system-critical events.
// Other event-handling should be encapsulated into standalone derivers.
func (n *OpNode) onEvent(ev event.Event) bool {
	switch x := ev.(type) {
	case rollup.CriticalErrorEvent:
		n.log.Error("Critical error", "err", x.Err)
		n.cancel(fmt.Errorf("critical error: %w", x.Err))
		return true
	default:
		return false
	}
}

func (n *OpNode) OnNewL1Head(ctx context.Context, sig eth.L1BlockRef) {
	n.tracer.OnNewL1Head(ctx, sig)

	if n.l2Driver == nil {
		return
	}
	// Pass on the event to the core Engine
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	if err := n.l2Driver.OnL1Head(ctx, sig); err != nil {
		n.log.Warn("failed to notify engine driver of L1 head change", "err", err)
	}
}

func (n *OpNode) OnNewL1Safe(ctx context.Context, sig eth.L1BlockRef) {
	if n.l2Driver == nil {
		return
	}
	// Pass on the event to the core Engine
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	if err := n.l2Driver.OnL1Safe(ctx, sig); err != nil {
		n.log.Warn("failed to notify engine driver of L1 safe block change", "err", err)
	}
}

func (n *OpNode) OnNewL1Finalized(ctx context.Context, sig eth.L1BlockRef) {
	if n.l2Driver == nil {
		return
	}
	// Pass on the event to the core Engine
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	if err := n.l2Driver.OnL1Finalized(ctx, sig); err != nil {
		n.log.Warn("failed to notify engine driver of L1 finalized block change", "err", err)
	}
}

func (n *OpNode) PublishL2Payload(ctx context.Context, envelope *eth.ExecutionPayloadEnvelope) error {
	n.tracer.OnPublishL2Payload(ctx, envelope)

	// After block production, broadcast to other validator nodes via P2P
	// Other validators receive and synchronize blocks via OnUnsafeL2Payload
	if p2pNode := n.getP2PNodeIfEnabled(); p2pNode != nil {
		if n.p2pSigner == nil {
			return fmt.Errorf("node has no p2p signer, payload %s cannot be published", envelope.ID())
		}
		n.log.Info("Publishing signed execution payload on p2p", "id", envelope.ID())

		// Notify manager that block production is complete
		if n.managerClient != nil {
			blockNumber := uint64(envelope.ExecutionPayload.BlockNumber)
			if err := n.managerClient.SendValidatorStatus(blockNumber, "done"); err != nil {
				n.log.Warn("Failed to send done status to manager", "err", err)
			}
		}

		return p2pNode.GossipOut().SignAndPublishL2Payload(ctx, envelope, n.p2pSigner)
	}
	// if p2p is not enabled then we just don't publish the payload
	return nil
}

func (n *OpNode) OnUnsafeL2Payload(ctx context.Context, from peer.ID, envelope *eth.ExecutionPayloadEnvelope) error {
	// ignore if it's from ourselves
	if p2pNode := n.getP2PNodeIfEnabled(); p2pNode != nil && from == p2pNode.Host().ID() {
		return nil
	}

	n.tracer.OnUnsafeL2Payload(ctx, from, envelope)

	n.log.Info("Received signed execution payload from p2p", "id", envelope.ExecutionPayload.ID(), "peer", from,
		"txs", len(envelope.ExecutionPayload.Transactions))

	// Pass on the event to the core Engine
	ctx, cancel := context.WithTimeout(ctx, time.Second*30)
	defer cancel()

	if err := n.l2Driver.OnUnsafeL2Payload(ctx, envelope); err != nil {
		n.log.Warn("failed to notify engine driver of new core payload", "err", err, "id", envelope.ExecutionPayload.ID())
	}

	return nil
}

func (n *OpNode) OnUnsafeTransactions(ctx context.Context, from peer.ID, txs []*types.Transaction) error {
	if p2pNode := n.getP2PNodeIfEnabled(); p2pNode != nil && from == p2pNode.Host().ID() {
		return nil
	}

	n.log.Info("Received transactions from P2P", "count", len(txs), "peer", from)

	isProducer, err := n.isCurrentBlockProducer(ctx)
	if err != nil {
		n.log.Warn("Failed to determine if current block producer", "err", err)
		return nil
	}

	if !isProducer {
		n.log.Debug("Not current block producer, ignoring P2P transactions")
		return nil
	}

	if n.l2Source == nil || n.l2Source.EngineAPIClient == nil || n.l2Source.EngineAPIClient.RPC == nil {
		return fmt.Errorf("L2 source not available")
	}

	n.log.Info("Current block producer: adding P2P transactions to local txpool", "count", len(txs))
	for _, tx := range txs {
		txData, err := tx.MarshalBinary()
		if err != nil {
			n.log.Warn("Failed to marshal transaction", "hash", tx.Hash(), "err", err)
			continue
		}
		var result common.Hash
		if err := n.l2Source.EngineAPIClient.RPC.CallContext(ctx, &result, "eth_sendRawTransaction", hexutil.Encode(txData)); err != nil {
			n.log.Debug("Failed to add transaction to txpool (may already exist)", "hash", tx.Hash(), "err", err)
		} else {
			n.log.Debug("Added transaction to txpool from P2P", "hash", tx.Hash())
		}
	}

	return nil
}

func (n *OpNode) PublishTransactions(ctx context.Context, txs []*types.Transaction) error {
	if len(txs) == 0 {
		return nil
	}

	isProducer, err := n.isCurrentBlockProducer(ctx)
	if err != nil {
		n.log.Warn("Failed to determine if current block producer", "err", err)
		isProducer = false
	}

	if isProducer {
		if n.l2Source == nil || n.l2Source.EngineAPIClient == nil || n.l2Source.EngineAPIClient.RPC == nil {
			return fmt.Errorf("L2 source not available")
		}

		n.log.Info("Current block producer: adding transactions to local txpool", "count", len(txs))
		for _, tx := range txs {
			txData, err := tx.MarshalBinary()
			if err != nil {
				n.log.Warn("Failed to marshal transaction", "hash", tx.Hash(), "err", err)
				continue
			}

			var result common.Hash
			if err := n.l2Source.EngineAPIClient.RPC.CallContext(ctx, &result, "eth_sendRawTransaction", hexutil.Encode(txData)); err != nil {
				n.log.Debug("Failed to add transaction to txpool (may already exist)", "hash", tx.Hash(), "err", err)
			} else {
				n.log.Debug("Added transaction to txpool", "hash", tx.Hash())
			}
		}
		return nil
	}

	p2pNode := n.getP2PNodeIfEnabled()
	if p2pNode == nil {
		return fmt.Errorf("P2P not enabled, cannot forward transactions")
	}

	n.log.Info("Not current block producer: forwarding transactions via P2P", "count", len(txs))
	return p2pNode.GossipOut().PublishTransactions(ctx, txs)
}

// isCurrentBlockProducer checks if this node is the current block producer based on epoch schedule
func (n *OpNode) isCurrentBlockProducer(ctx context.Context) (bool, error) {
	if !n.cfg.Driver.PosMode {
		return false, nil
	}

	n.epochScheduleMu.RLock()
	schedule := n.currentEpochSchedule
	n.epochScheduleMu.RUnlock()

	if schedule == nil {
		// No epoch schedule received yet, cannot determine
		return false, nil
	}

	if n.l2Source == nil {
		return false, nil
	}

	head, err := n.l2Source.L2BlockRefByLabel(ctx, eth.Unsafe)
	if err != nil {
		n.log.Debug("Failed to get current block number", "err", err)
		return false, nil
	}

	nextBlockNumber := head.Number + 1

	for _, assignment := range schedule.Assignments {
		if assignment.BlockNumber == nextBlockNumber {
			isProducer := assignment.Validator == n.cfg.P2PSignerAddress
			if isProducer {
				n.log.Debug("This node is the current block producer", "block_number", nextBlockNumber)
			}
			return isProducer, nil
		}
	}

	n.log.Debug("No assignment found for next block, not the current producer", "next_block", nextBlockNumber)
	return false, nil
}

func (n *OpNode) IsBlockProducerFor(nextBlock uint64) bool {
	if !n.cfg.Driver.PosMode {
		return false
	}
	n.epochScheduleMu.RLock()
	schedule := n.currentEpochSchedule
	n.epochScheduleMu.RUnlock()
	if schedule == nil {
		return false
	}
	for _, assignment := range schedule.Assignments {
		if assignment.BlockNumber == nextBlock {
			return assignment.Validator == n.cfg.P2PSignerAddress
		}
	}
	return false
}

func (n *OpNode) RequestL2Range(ctx context.Context, start, end eth.L2BlockRef) error {
	if p2pNode := n.getP2PNodeIfEnabled(); p2pNode != nil && p2pNode.AltSyncEnabled() {
		if start.Number != 0 && unixTimeStale(start.Time, 12*time.Hour) {
			n.log.Debug(
				"ignoring request to sync core range, timestamp is too old for p2p",
				"start", start,
				"end", end,
				"start_time", start.Time)
			return nil
		}
		return p2pNode.RequestL2Range(ctx, start, end)
	}
	n.log.Debug("ignoring request to sync core range, no sync method available", "start", start, "end", end)
	return nil
}

// unixTimeStale returns true if the unix timestamp is before the current time minus the supplied duration.
func unixTimeStale(timestamp uint64, duration time.Duration) bool {
	return time.Unix(int64(timestamp), 0).Before(time.Now().Add(-1 * duration))
}

func (n *OpNode) P2P() p2p.Node {
	return n.getP2PNodeIfEnabled()
}

func (n *OpNode) RuntimeConfig() ReadonlyRuntimeConfig {
	return n.runCfg
}

// Stop stops the node and closes all resources.
// If the provided ctx is expired, the node will accelerate the stop where possible, but still fully close.
func (n *OpNode) Stop(ctx context.Context) error {
	if n.closed.Load() {
		return ErrAlreadyClosed
	}

	var result *multierror.Error

	if n.server != nil {
		if err := n.server.Stop(); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close RPC server: %w", err))
		}
	}

	// Stop sequencer and report last hash. l2Driver can be nil if we're cleaning up a failed init.
	if n.l2Driver != nil {
		latestHead, err := n.l2Driver.StopSequencer(ctx)
		switch {
		case errors.Is(err, sequencing.ErrSequencerNotEnabled):
		case errors.Is(err, driver.ErrSequencerAlreadyStopped):
			n.log.Info("stopping node: sequencer already stopped", "latestHead", latestHead)
		case err == nil:
			n.log.Info("stopped sequencer", "latestHead", latestHead)
		default:
			result = multierror.Append(result, fmt.Errorf("error stopping sequencer: %w", err))
		}
	}

	n.p2pMu.Lock()
	if n.p2pNode != nil {
		if err := n.p2pNode.Close(); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close p2p node: %w", err))
		}
		// Prevent further use of p2p.
		n.p2pNode = nil
	}
	n.p2pMu.Unlock()

	if n.p2pSigner != nil {
		if err := n.p2pSigner.Close(); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close p2p signer: %w", err))
		}
	}

	if n.resourcesClose != nil {
		n.resourcesClose()
	}

	// stop L1 heads feed
	if n.l1HeadsSub != nil {
		n.l1HeadsSub.Unsubscribe()
	}
	// stop polling for L1 safe-head changes
	if n.l1SafeSub != nil {
		n.l1SafeSub.Unsubscribe()
	}
	// stop polling for L1 finalized-head changes
	if n.l1FinalizedSub != nil {
		n.l1FinalizedSub.Unsubscribe()
	}

	// close core driver
	if n.l2Driver != nil {
		if err := n.l2Driver.Close(); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close core engine driver cleanly: %w", err))
		}
	}

	// close the interop sub system
	if n.interopSys != nil {
		if err := n.interopSys.Stop(ctx); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close interop sub-system: %w", err))
		}
	}

	// close manager client
	if n.managerClient != nil {
		n.managerClient.Stop()
	}

	if n.eventSys != nil {
		n.eventSys.Stop()
	}

	if n.safeDB != nil {
		if err := n.safeDB.Close(); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close safe head db: %w", err))
		}
	}

	// Wait for the runtime config loader to be done using the data sources before closing them
	if n.runtimeConfigReloaderDone != nil {
		<-n.runtimeConfigReloaderDone
	}

	// close core engine RPC client
	if n.l2Source != nil {
		n.l2Source.Close()
	}

	if result == nil { // mark as closed if we successfully fully closed
		n.closed.Store(true)
	}

	if n.halted.Load() {
		// if we had a halt upon initialization, idle for a while, with open metrics, to prevent a rapid restart-loop
		tim := time.NewTimer(time.Minute * 5)
		n.log.Warn("halted, idling to avoid immediate shutdown repeats")
		defer tim.Stop()
		select {
		case <-tim.C:
		case <-ctx.Done():
		}
	}

	// Close metrics and pprof only after we are done idling
	if n.pprofService != nil {
		if err := n.pprofService.Stop(ctx); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close pprof server: %w", err))
		}
	}
	if n.metricsSrv != nil {
		if err := n.metricsSrv.Stop(ctx); err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to close metrics server: %w", err))
		}
	}

	return result.ErrorOrNil()
}

func (n *OpNode) Stopped() bool {
	return n.closed.Load()
}

func (n *OpNode) HTTPEndpoint() string {
	if n.server == nil {
		return ""
	}
	return fmt.Sprintf("http://%s", n.server.Endpoint())
}

func (n *OpNode) HTTPPort() (int, error) {
	return n.server.Port()
}

func (n *OpNode) InteropRPC() (rpcEndpoint string, jwtSecret eth.Bytes32) {
	m, ok := n.interopSys.(*managed.ManagedMode)
	if !ok {
		return "", [32]byte{}
	}
	return m.WSEndpoint(), m.JWTSecret()
}

func (n *OpNode) InteropRPCPort() (int, error) {
	m, ok := n.interopSys.(*managed.ManagedMode)
	if !ok {
		return 0, fmt.Errorf("failed to fetch interop port for dn-node")
	}
	return m.WSPort()
}

func (n *OpNode) getP2PNodeIfEnabled() *p2p.NodeP2P {
	if !n.p2pEnabled() {
		return nil
	}

	n.p2pMu.Lock()
	defer n.p2pMu.Unlock()
	return n.p2pNode
}

func (n *OpNode) GetEpochForBlock(blockNumber uint64) (epoch uint64, startBlock uint64, endBlock uint64, found bool) {
	n.epochScheduleMu.RLock()
	defer n.epochScheduleMu.RUnlock()

	if n.currentEpochSchedule != nil {
		if blockNumber >= n.currentEpochSchedule.StartBlock && blockNumber <= n.currentEpochSchedule.EndBlock {
			return n.currentEpochSchedule.Epoch, n.currentEpochSchedule.StartBlock, n.currentEpochSchedule.EndBlock, true
		}
	}

	for i := len(n.epochScheduleHistory) - 1; i >= 0; i-- {
		hist := n.epochScheduleHistory[i]
		if hist != nil && blockNumber >= hist.StartBlock && blockNumber <= hist.EndBlock {
			return hist.Epoch, hist.StartBlock, hist.EndBlock, true
		}
	}

	return 0, 0, 0, false
}
