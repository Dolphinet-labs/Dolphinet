package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/sources"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/derive"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/event"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/sync"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/clock"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/eth"
)

type syncStatusEnum int

const (
	syncStatusCL syncStatusEnum = iota
	// We transition between the 4 EL states linearly. We spend the majority of the time in the second & fourth.
	// We only want to EL sync if there is no finalized block & once we finish EL sync we need to mark the last block
	// as finalized so we can switch to consolidation
	// TODO(protocol-quest#91): We can restart EL sync & still consolidate if there finalized blocks on the execution client if the
	// execution client is running in archive mode. In some cases we may want to switch back from CL to EL sync, but that is complicated.
	syncStatusWillStartEL               // First if we are directed to EL sync, check that nothing has been finalized yet
	syncStatusStartedEL                 // Perform our EL sync
	syncStatusFinishedELButNotFinalized // EL sync is done, but we need to mark the final sync block as finalized
	syncStatusFinishedEL                // EL sync is done & we should be performing consolidation
)

var ErrNoFCUNeeded = errors.New("no FCU call was needed")

type ExecEngine interface {
	GetPayload(ctx context.Context, payloadInfo eth.PayloadInfo) (*eth.ExecutionPayloadEnvelope, error)
	ForkchoiceUpdate(ctx context.Context, state *eth.ForkchoiceState, attr *eth.PayloadAttributes) (*eth.ForkchoiceUpdatedResult, error)
	NewPayload(ctx context.Context, payload *eth.ExecutionPayload, parentBeaconBlockRoot *common.Hash) (*eth.PayloadStatusV1, error)
	L2BlockRefByLabel(ctx context.Context, label eth.BlockLabel) (eth.L2BlockRef, error)
	L2BlockRefByNumber(ctx context.Context, num uint64) (eth.L2BlockRef, error)
}

type EpochInfoGetter interface {
	GetEpochForBlock(blockNumber uint64) (epoch uint64, startBlock uint64, endBlock uint64, found bool)
}

type EngineController struct {
	engine     ExecEngine // Underlying execution engine RPC
	elClient   *sources.EthClient
	log        log.Logger
	metrics    derive.Metrics
	syncCfg    *sync.Config
	syncStatus syncStatusEnum
	chainSpec  *rollup.ChainSpec
	rollupCfg  *rollup.Config
	elStart    time.Time
	clock      clock.Clock

	emitter event.Emitter

	// Block Head State
	unsafeHead eth.L2BlockRef
	// Cross-verified unsafeHead, always equal to unsafeHead pre-interop
	crossUnsafeHead eth.L2BlockRef
	// Pending localSafeHead
	// core block processed from the middle of a span batch,
	// but not marked as the safe block yet.
	pendingSafeHead eth.L2BlockRef
	// Derived from L1, and known to be a completed span-batch,
	// but not cross-verified yet.
	localSafeHead eth.L2BlockRef
	// Derived from L1 and cross-verified to have cross-safe dependencies.
	safeHead eth.L2BlockRef
	// Derived from finalized L1 data,
	// and cross-verified to only have finalized dependencies.
	finalizedHead eth.L2BlockRef
	// The unsafe head to roll back to,
	// after the pendingSafeHead fails to become safe.
	// This is changing in the Holocene fork.
	backupUnsafeHead eth.L2BlockRef

	needFCUCall bool
	// Track when the rollup node changes the forkchoice to restore previous
	// known unsafe chain. e.g. Unsafe Reorg caused by Invalid span batch.
	// This update does not retry except engine returns non-input error
	// because engine may forgot backupUnsafeHead or backupUnsafeHead is not part
	// of the chain.
	needFCUCallForBackupUnsafeReorg bool

	// Epoch info getter for calculating safe and finalized blocks based on epochs
	epochInfoGetter EpochInfoGetter
}

func NewEngineController(engine ExecEngine, elClient *sources.EthClient, log log.Logger, metrics derive.Metrics,
	rollupCfg *rollup.Config, syncCfg *sync.Config, emitter event.Emitter,
) *EngineController {
	syncStatus := syncStatusCL
	if syncCfg.SyncMode == sync.ELSync {
		syncStatus = syncStatusWillStartEL
	}

	return &EngineController{
		engine:     engine,
		elClient:   elClient,
		log:        log,
		metrics:    metrics,
		chainSpec:  rollup.NewChainSpec(rollupCfg),
		rollupCfg:  rollupCfg,
		syncCfg:    syncCfg,
		syncStatus: syncStatus,
		clock:      clock.SystemClock,
		emitter:    emitter,
	}
}

func (e *EngineController) SetEpochInfoGetter(getter EpochInfoGetter) {
	e.epochInfoGetter = getter
}

func (e *EngineController) UnsafeL2Head() eth.L2BlockRef {
	return e.unsafeHead
}

func (e *EngineController) CrossUnsafeL2Head() eth.L2BlockRef {
	return e.crossUnsafeHead
}

func (e *EngineController) PendingSafeL2Head() eth.L2BlockRef {
	return e.pendingSafeHead
}

func (e *EngineController) LocalSafeL2Head() eth.L2BlockRef {
	return e.localSafeHead
}

func (e *EngineController) SafeL2Head() eth.L2BlockRef {
	return e.safeHead
}

func (e *EngineController) Finalized() eth.L2BlockRef {
	return e.finalizedHead
}

func (e *EngineController) BackupUnsafeL2Head() eth.L2BlockRef {
	return e.backupUnsafeHead
}

func (e *EngineController) IsEngineSyncing() bool {
	return e.syncStatus == syncStatusWillStartEL || e.syncStatus == syncStatusStartedEL || e.syncStatus == syncStatusFinishedELButNotFinalized
}

// Setters

// SetFinalizedHead implements LocalEngineControl.
func (e *EngineController) SetFinalizedHead(r eth.L2BlockRef) {
	if e.epochInfoGetter != nil && e.unsafeHead != (eth.L2BlockRef{}) {
		currentEpoch, _, _, found := e.epochInfoGetter.GetEpochForBlock(e.unsafeHead.Number)
		if found && currentEpoch < 2 {
			e.log.Debug("Skipping finalized head update when current epoch < 2", "current_epoch", currentEpoch, "block", r.Number, "current_head", e.unsafeHead.Number)
			return
		}
	}
	e.metrics.RecordL2Ref("l2_finalized", r)
	e.finalizedHead = r
	e.needFCUCall = true
}

// SetPendingSafeL2Head implements LocalEngineControl.
func (e *EngineController) SetPendingSafeL2Head(r eth.L2BlockRef) {
	e.metrics.RecordL2Ref("l2_pending_safe", r)
	e.pendingSafeHead = r
}

// SetLocalSafeHead sets the local-safe head.
func (e *EngineController) SetLocalSafeHead(r eth.L2BlockRef) {
	e.metrics.RecordL2Ref("l2_local_safe", r)
	e.localSafeHead = r
}

// SetSafeHead sets the cross-safe head.
func (e *EngineController) SetSafeHead(r eth.L2BlockRef) {
	if e.epochInfoGetter != nil && e.unsafeHead != (eth.L2BlockRef{}) {
		currentEpoch, _, _, found := e.epochInfoGetter.GetEpochForBlock(e.unsafeHead.Number)
		if found && currentEpoch < 1 {
			e.log.Debug("Skipping safe head update when current epoch < 1", "current_epoch", currentEpoch, "block", r.Number, "current_head", e.unsafeHead.Number)
			return
		}
	}
	e.metrics.RecordL2Ref("l2_safe", r)
	e.safeHead = r
	e.needFCUCall = true
}

// SetUnsafeHead sets the local-unsafe head.
func (e *EngineController) SetUnsafeHead(r eth.L2BlockRef) {
	e.metrics.RecordL2Ref("l2_unsafe", r)
	e.unsafeHead = r
	e.needFCUCall = true
	e.chainSpec.CheckForkActivation(e.log, r)
}

// SetCrossUnsafeHead the cross-unsafe head.
func (e *EngineController) SetCrossUnsafeHead(r eth.L2BlockRef) {
	e.metrics.RecordL2Ref("l2_cross_unsafe", r)
	e.crossUnsafeHead = r
}

// SetBackupUnsafeL2Head implements LocalEngineControl.
func (e *EngineController) SetBackupUnsafeL2Head(r eth.L2BlockRef, triggerReorg bool) {
	e.metrics.RecordL2Ref("l2_backup_unsafe", r)
	e.backupUnsafeHead = r
	e.needFCUCallForBackupUnsafeReorg = triggerReorg
}

// logSyncProgressMaybe helps log forkchoice state-changes when applicable.
// First, the pre-state is registered.
// A callback is returned to then log the changes to the pre-state, if any.
func (e *EngineController) logSyncProgressMaybe() func() {
	prevFinalized := e.finalizedHead
	prevSafe := e.safeHead
	prevPendingSafe := e.pendingSafeHead
	prevUnsafe := e.unsafeHead
	prevBackupUnsafe := e.backupUnsafeHead
	return func() {
		// if forkchoice still needs to be updated, then the last change was unsuccessful, thus no progress to log.
		if e.needFCUCall || e.needFCUCallForBackupUnsafeReorg {
			return
		}
		var reason string
		if prevFinalized != e.finalizedHead {
			reason = "finalized block"
		} else if prevSafe != e.safeHead {
			if prevSafe == prevUnsafe {
				reason = "derived safe block from L1"
			} else {
				reason = "consolidated block with L1"
			}
		} else if prevUnsafe != e.unsafeHead {
			reason = "new chain head block"
		} else if prevPendingSafe != e.pendingSafeHead {
			reason = "pending new safe block"
		} else if prevBackupUnsafe != e.backupUnsafeHead {
			reason = "new backup unsafe block"
		}
		if reason != "" {
			e.log.Info("Sync progress",
				"reason", reason,
				"l2_finalized", e.finalizedHead,
				"l2_safe", e.safeHead,
				"l2_pending_safe", e.pendingSafeHead,
				"l2_unsafe", e.unsafeHead,
				"l2_backup_unsafe", e.backupUnsafeHead,
				"l2_time", e.UnsafeL2Head().Time,
			)
		}
	}
}

// Misc Setters only used by the engine queue

// checkNewPayloadStatus checks returned status of engine_newPayloadV1 request for next unsafe payload.
// It returns true if the status is acceptable.
func (e *EngineController) checkNewPayloadStatus(status eth.ExecutePayloadStatus) bool {
	if e.syncCfg.SyncMode == sync.ELSync {
		if status == eth.ExecutionValid && e.syncStatus == syncStatusStartedEL {
			e.syncStatus = syncStatusFinishedELButNotFinalized
		}
		// Allow SYNCING and ACCEPTED if engine EL sync is enabled
		return status == eth.ExecutionValid || status == eth.ExecutionSyncing || status == eth.ExecutionAccepted
	}
	// In CL sync mode, also allow ExecutionSyncing when parent blocks are missing
	// The missing blocks will be requested via P2P, and we can retry processing the payload later
	if status == eth.ExecutionSyncing {
		e.log.Debug("Engine is syncing (missing parent blocks), will wait for blocks to arrive via P2P")
		return true
	}
	return status == eth.ExecutionValid
}

// checkForkchoiceUpdatedStatus checks returned status of engine_forkchoiceUpdatedV1 request for next unsafe payload.
// It returns true if the status is acceptable.
func (e *EngineController) checkForkchoiceUpdatedStatus(status eth.ExecutePayloadStatus) bool {
	if e.syncCfg.SyncMode == sync.ELSync {
		if status == eth.ExecutionValid && e.syncStatus == syncStatusStartedEL {
			e.syncStatus = syncStatusFinishedELButNotFinalized
		}
		// Allow SYNCING if engine P2P sync is enabled
		return status == eth.ExecutionValid || status == eth.ExecutionSyncing
	}
	return status == eth.ExecutionValid
}

// initializeUnknowns is important to give the dn-node EngineController engine state.
// Pre-interop, the initial reset triggered a find-sync-start, and filled the forkchoice.
// This still happens, but now overrides what may be initialized here.
// Post-interop, the dn-supervisor may diff the forkchoice state against the supervisor DB,
// to determine where to perform the initial reset to.
func (e *EngineController) initializeUnknowns(ctx context.Context) error {
	if e.unsafeHead == (eth.L2BlockRef{}) {
		ref, err := e.engine.L2BlockRefByLabel(ctx, eth.Unsafe)
		if err != nil {
			return fmt.Errorf("failed to load local-unsafe head: %w", err)
		}
		e.SetUnsafeHead(ref)
		e.log.Info("Loaded initial local-unsafe block ref", "local_unsafe", ref)
	}
	var finalizedRef eth.L2BlockRef
	if e.finalizedHead == (eth.L2BlockRef{}) {
		var err error
		finalizedRef, err = e.engine.L2BlockRefByLabel(ctx, eth.Finalized)
		if err != nil {
			return fmt.Errorf("failed to load finalized head: %w", err)
		}
		e.SetFinalizedHead(finalizedRef)
		e.log.Info("Loaded initial finalized block ref", "finalized", finalizedRef)
	}
	if e.safeHead == (eth.L2BlockRef{}) {
		ref, err := e.engine.L2BlockRefByLabel(ctx, eth.Safe)
		if err != nil {
			if errors.Is(err, ethereum.NotFound) {
				// If the engine doesn't have a safe head, then we can use the finalized head
				e.SetSafeHead(finalizedRef)
				e.log.Info("Loaded initial cross-safe block from finalized", "cross_safe", finalizedRef)
			} else {
				return fmt.Errorf("failed to load cross-safe head: %w", err)
			}
		} else {
			e.SetSafeHead(ref)
			e.log.Info("Loaded initial cross-safe block ref", "cross_safe", ref)
		}
	}
	if e.crossUnsafeHead == (eth.L2BlockRef{}) {
		e.SetCrossUnsafeHead(e.safeHead) // preserve cross-safety, don't fall back to a non-cross safety level
		e.log.Info("Set initial cross-unsafe block ref to match cross-safe", "cross_unsafe", e.safeHead)
	}
	if e.localSafeHead == (eth.L2BlockRef{}) {
		e.SetLocalSafeHead(e.safeHead)
		e.log.Info("Set initial local-safe block ref to match cross-safe", "local_safe", e.safeHead)
	}
	return nil
}

// TryUpdateEngine attempts to update the engine with the current forkchoice state of the rollup node,
// this is a no-op if the nodes already agree on the forkchoice state.
func (e *EngineController) TryUpdateEngine(ctx context.Context) error {
	if !e.needFCUCall {
		return ErrNoFCUNeeded
	}
	if e.IsEngineSyncing() {
		e.log.Warn("Attempting to update forkchoice state while EL syncing")
	}
	if err := e.initializeUnknowns(ctx); err != nil {
		return derive.NewTemporaryError(fmt.Errorf("cannot update engine until engine forkchoice is initialized: %w", err))
	}
	if e.unsafeHead.Number < e.finalizedHead.Number {
		err := fmt.Errorf("invalid forkchoice state, unsafe head %s is behind finalized head %s", e.unsafeHead, e.finalizedHead)
		e.emitter.Emit(rollup.CriticalErrorEvent{Err: err}) // make the node exit, things are very wrong.
		return err
	}
	fc := eth.ForkchoiceState{
		HeadBlockHash:      e.unsafeHead.Hash,
		SafeBlockHash:      e.safeHead.Hash,
		FinalizedBlockHash: e.finalizedHead.Hash,
	}
	logFn := e.logSyncProgressMaybe()
	defer logFn()
	fcRes, err := e.engine.ForkchoiceUpdate(ctx, &fc, nil)
	if err != nil {
		var rpcErr rpc.Error
		if errors.As(err, &rpcErr) {
			switch eth.ErrorCode(rpcErr.ErrorCode()) {
			case eth.InvalidForkchoiceState:
				return derive.NewResetError(fmt.Errorf("forkchoice update was inconsistent with engine, need reset to resolve: %w", err))
			default:
				return derive.NewTemporaryError(fmt.Errorf("unexpected error code in forkchoice-updated response: %w", err))
			}
		} else {
			return derive.NewTemporaryError(fmt.Errorf("failed to sync forkchoice with engine: %w", err))
		}
	}
	if fcRes.PayloadStatus.Status == eth.ExecutionValid {
		e.emitter.Emit(ForkchoiceUpdateEvent{
			UnsafeL2Head:    e.unsafeHead,
			SafeL2Head:      e.safeHead,
			FinalizedL2Head: e.finalizedHead,
		})
	}
	if e.unsafeHead == e.safeHead && e.safeHead == e.pendingSafeHead {
		// Remove backupUnsafeHead because this backup will be never used after consolidation.
		e.SetBackupUnsafeL2Head(eth.L2BlockRef{}, false)
	}
	e.needFCUCall = false
	return nil
}

func (e *EngineController) InsertUnsafePayload(ctx context.Context, envelope *eth.ExecutionPayloadEnvelope, ref eth.L2BlockRef) error {
	// Check if there is a finalized head once when doing EL sync. If so, transition to CL sync
	if e.syncStatus == syncStatusWillStartEL {
		b, err := e.engine.L2BlockRefByLabel(ctx, eth.Finalized)
		rollupGenesisIsFinalized := b.Hash == e.rollupCfg.Genesis.L2.Hash
		if errors.Is(err, ethereum.NotFound) || rollupGenesisIsFinalized || e.syncCfg.SupportsPostFinalizationELSync {
			e.syncStatus = syncStatusStartedEL
			e.log.Info("Starting EL sync")
			e.elStart = e.clock.Now()
		} else if err == nil {
			e.syncStatus = syncStatusFinishedEL
			e.log.Info("Skipping EL sync and going straight to CL sync because there is a finalized block", "id", b.ID())
			return nil
		} else {
			return derive.NewTemporaryError(fmt.Errorf("failed to fetch finalized head: %w", err))
		}
	}
	// Insert the payload & then call FCU
	newPayloadStart := time.Now()
	status, err := e.engine.NewPayload(ctx, envelope.ExecutionPayload, envelope.ParentBeaconBlockRoot)
	if err != nil {
		return derive.NewTemporaryError(fmt.Errorf("failed to update insert payload: %w", err))
	}
	if status.Status == eth.ExecutionInvalid {
		e.emitter.Emit(PayloadInvalidEvent{Envelope: envelope, Err: eth.NewPayloadErr(envelope.ExecutionPayload, status)})
	}
	if !e.checkNewPayloadStatus(status.Status) {
		payload := envelope.ExecutionPayload
		return derive.NewTemporaryError(fmt.Errorf("cannot process unsafe payload: new - %v; parent: %v; err: %w",
			payload.ID(), payload.ParentID(), eth.NewPayloadErr(payload, status)))
	}
	newPayloadFinish := time.Now()

	// If engine is syncing (missing parent blocks), don't update forkchoice yet
	// Wait for missing blocks to arrive via P2P, then retry processing this payload
	if status.Status == eth.ExecutionSyncing {
		e.log.Debug("Engine is syncing (missing parent blocks), payload will be retried once blocks arrive",
			"payload", envelope.ExecutionPayload.ID(),
			"parent", envelope.ExecutionPayload.ParentID())
		// Return a temporary error to indicate the payload needs to be retried later
		// This prevents the payload from being marked as "successfully processed" in events.go
		// The payload will remain in the queue and be retried once parent blocks arrive
		return derive.NewTemporaryError(fmt.Errorf("engine is syncing (missing parent blocks), payload will be retried once blocks arrive: payload=%v, parent=%v",
			envelope.ExecutionPayload.ID(), envelope.ExecutionPayload.ParentID()))
	}

	// Mark the new payload as valid
	fc := eth.ForkchoiceState{
		HeadBlockHash:      envelope.ExecutionPayload.BlockHash,
		SafeBlockHash:      e.safeHead.Hash,
		FinalizedBlockHash: e.finalizedHead.Hash,
	}
	if e.syncStatus == syncStatusFinishedELButNotFinalized {
		fc.SafeBlockHash = envelope.ExecutionPayload.BlockHash
		fc.FinalizedBlockHash = envelope.ExecutionPayload.BlockHash
		e.SetUnsafeHead(ref) // ensure that the unsafe head stays ahead of safe/finalized labels.
		e.emitter.Emit(UnsafeUpdateEvent{Ref: ref})
		e.SetLocalSafeHead(ref)
		e.SetSafeHead(ref)
		e.emitter.Emit(CrossSafeUpdateEvent{LocalSafe: ref, CrossSafe: ref})
		e.SetFinalizedHead(ref)
	}
	logFn := e.logSyncProgressMaybe()
	defer logFn()
	fcu2Start := time.Now()
	fcRes, err := e.engine.ForkchoiceUpdate(ctx, &fc, nil)
	if err != nil {
		var rpcErr rpc.Error
		if errors.As(err, &rpcErr) {
			switch eth.ErrorCode(rpcErr.ErrorCode()) {
			case eth.InvalidForkchoiceState:
				return derive.NewResetError(fmt.Errorf("pre-unsafe-block forkchoice update was inconsistent with engine, need reset to resolve: %w", err))
			default:
				return derive.NewTemporaryError(fmt.Errorf("unexpected error code in forkchoice-updated response: %w", err))
			}
		} else {
			return derive.NewTemporaryError(fmt.Errorf("failed to update forkchoice to prepare for new unsafe payload: %w", err))
		}
	}
	if !e.checkForkchoiceUpdatedStatus(fcRes.PayloadStatus.Status) {
		payload := envelope.ExecutionPayload
		return derive.NewTemporaryError(fmt.Errorf("cannot prepare unsafe chain for new payload: new - %v; parent: %v; err: %w",
			payload.ID(), payload.ParentID(), eth.ForkchoiceUpdateErr(fcRes.PayloadStatus)))
	}
	fcu2Finish := time.Now()
	e.SetUnsafeHead(ref)
	e.needFCUCall = false
	e.emitter.Emit(UnsafeUpdateEvent{Ref: ref})

	if e.syncStatus == syncStatusFinishedELButNotFinalized {
		e.log.Info("Finished EL sync", "sync_duration", e.clock.Since(e.elStart), "finalized_block", ref.ID().String())
		e.syncStatus = syncStatusFinishedEL
	}

	if fcRes.PayloadStatus.Status == eth.ExecutionValid || fcRes.PayloadStatus.Status == eth.ExecutionSyncing {
		safeRef, finalizedRef, shouldUpdateSafe, shouldUpdateFinalized := e.calculateSafeAndFinalized(ctx, ref)
		if shouldUpdateSafe && safeRef != (eth.L2BlockRef{}) {
			e.emitter.Emit(PromoteLocalSafeEvent{
				Ref: safeRef,
			})
		}
		if shouldUpdateFinalized && finalizedRef != (eth.L2BlockRef{}) {
			e.SetFinalizedHead(finalizedRef)
		}
		e.emitter.Emit(ForkchoiceUpdateEvent{
			UnsafeL2Head:    e.unsafeHead,
			SafeL2Head:      e.safeHead,
			FinalizedL2Head: e.finalizedHead,
		})
	}

	totalTime := fcu2Finish.Sub(newPayloadStart)
	e.log.Info("Inserted new core unsafe block (synchronous)",
		"hash", envelope.ExecutionPayload.BlockHash,
		"number", uint64(envelope.ExecutionPayload.BlockNumber),
		"newpayload_time", common.PrettyDuration(newPayloadFinish.Sub(newPayloadStart)),
		"fcu2_time", common.PrettyDuration(fcu2Finish.Sub(fcu2Start)),
		"total_time", common.PrettyDuration(totalTime),
		"mgas", float64(envelope.ExecutionPayload.GasUsed)/1000000,
		"mgasps", float64(envelope.ExecutionPayload.GasUsed)*1000/float64(totalTime))

	return nil
}

// shouldTryBackupUnsafeReorg checks reorging(restoring) unsafe head to backupUnsafeHead is needed.
// Returns boolean which decides to trigger FCU.
func (e *EngineController) shouldTryBackupUnsafeReorg() bool {
	if !e.needFCUCallForBackupUnsafeReorg {
		return false
	}
	// This method must be never called when EL sync. If EL sync is in progress, early return.
	if e.IsEngineSyncing() {
		e.log.Warn("Attempting to unsafe reorg using backupUnsafe while EL syncing")
		return false
	}
	if e.BackupUnsafeL2Head() == (eth.L2BlockRef{}) { // sanity check backupUnsafeHead is there
		e.log.Warn("Attempting to unsafe reorg using backupUnsafe even though it is empty")
		e.SetBackupUnsafeL2Head(eth.L2BlockRef{}, false)
		return false
	}
	return true
}

// TryBackupUnsafeReorg attempts to reorg(restore) unsafe head to backupUnsafeHead.
// If succeeds, update current forkchoice state to the rollup node.
func (e *EngineController) TryBackupUnsafeReorg(ctx context.Context) (bool, error) {
	if !e.shouldTryBackupUnsafeReorg() {
		// Do not need to perform FCU.
		return false, nil
	}
	// Only try FCU once because execution engine may forgot backupUnsafeHead
	// or backupUnsafeHead is not part of the chain.
	// Exception: Retry when forkChoiceUpdate returns non-input error.
	e.needFCUCallForBackupUnsafeReorg = false
	// Reorg unsafe chain. Safe/Finalized chain will not be updated.
	e.log.Warn("trying to restore unsafe head", "backupUnsafe", e.backupUnsafeHead.ID(), "unsafe", e.unsafeHead.ID())
	fc := eth.ForkchoiceState{
		HeadBlockHash:      e.backupUnsafeHead.Hash,
		SafeBlockHash:      e.safeHead.Hash,
		FinalizedBlockHash: e.finalizedHead.Hash,
	}
	logFn := e.logSyncProgressMaybe()
	defer logFn()
	fcRes, err := e.engine.ForkchoiceUpdate(ctx, &fc, nil)
	if err != nil {
		var rpcErr rpc.Error
		if errors.As(err, &rpcErr) {
			switch eth.ErrorCode(rpcErr.ErrorCode()) {
			case eth.InvalidForkchoiceState:
				e.SetBackupUnsafeL2Head(eth.L2BlockRef{}, false)
				return true, derive.NewResetError(fmt.Errorf("forkchoice update was inconsistent with engine, need reset to resolve: %w", err))
			default:
				// Retry when forkChoiceUpdate returns non-input error.
				// Do not reset backupUnsafeHead because it will be used again.
				e.needFCUCallForBackupUnsafeReorg = true
				return true, derive.NewTemporaryError(fmt.Errorf("unexpected error code in forkchoice-updated response: %w", err))
			}
		} else {
			// Retry when forkChoiceUpdate returns non-input error.
			// Do not reset backupUnsafeHead because it will be used again.
			e.needFCUCallForBackupUnsafeReorg = true
			return true, derive.NewTemporaryError(fmt.Errorf("failed to sync forkchoice with engine: %w", err))
		}
	}
	if fcRes.PayloadStatus.Status == eth.ExecutionValid {
		e.emitter.Emit(ForkchoiceUpdateEvent{
			UnsafeL2Head:    e.backupUnsafeHead,
			SafeL2Head:      e.safeHead,
			FinalizedL2Head: e.finalizedHead,
		})
		// Execution engine accepted the reorg.
		e.log.Info("successfully reorged unsafe head using backupUnsafe", "unsafe", e.backupUnsafeHead.ID())
		e.SetUnsafeHead(e.BackupUnsafeL2Head())
		e.SetBackupUnsafeL2Head(eth.L2BlockRef{}, false)
		return true, nil
	}
	e.SetBackupUnsafeL2Head(eth.L2BlockRef{}, false)
	// Execution engine could not reorg back to previous unsafe head.
	return true, derive.NewTemporaryError(fmt.Errorf("cannot restore unsafe chain using backupUnsafe: err: %w",
		eth.ForkchoiceUpdateErr(fcRes.PayloadStatus)))
}

func (e *EngineController) calculateSafeAndFinalized(ctx context.Context, currentRef eth.L2BlockRef) (safeRef eth.L2BlockRef, finalizedRef eth.L2BlockRef, shouldUpdateSafe bool, shouldUpdateFinalized bool) {
	if e.epochInfoGetter == nil {
		e.log.Debug("No epoch info getter set, using current block for safe/finalized")
		return currentRef, currentRef, true, true
	}

	currentBlockNum := currentRef.Number
	currentEpoch, currentStartBlock, currentEndBlock, found := e.epochInfoGetter.GetEpochForBlock(currentBlockNum)
	if !found {
		if e.elClient != nil {
			e.log.Debug("Could not find epoch info for current block, trying to get safe/finalized from validator via RPC",
				"block_number", currentBlockNum)
			safeRefFromRPC, err := e.elClient.BlockRefByLabel(ctx, eth.Safe)
			if err != nil {
				e.log.Debug("Failed to get safe block from validator via RPC, using current block", "err", err)
				if e.safeHead != (eth.L2BlockRef{}) {
					safeRef = e.safeHead
				} else {
					safeRef = currentRef
				}
			} else {
				safeRef = eth.L2BlockRef{
					Hash:           safeRefFromRPC.Hash,
					Number:         safeRefFromRPC.Number,
					ParentHash:     safeRefFromRPC.ParentHash,
					Time:           safeRefFromRPC.Time,
					SequenceNumber: 0,
				}
				e.log.Debug("Got safe block from validator via RPC", "safe_block", safeRef.Number)
			}

			finalizedRefFromRPC, err := e.elClient.BlockRefByLabel(ctx, eth.Finalized)
			if err != nil {
				e.log.Debug("Failed to get finalized block from validator via RPC, keeping existing finalized", "err", err)
				if e.finalizedHead != (eth.L2BlockRef{}) {
					finalizedRef = e.finalizedHead
					return safeRef, finalizedRef, true, false
				}
				finalizedRef = e.finalizedHead
				return safeRef, finalizedRef, true, false
			}
			finalizedRef = eth.L2BlockRef{
				Hash:           finalizedRefFromRPC.Hash,
				Number:         finalizedRefFromRPC.Number,
				ParentHash:     finalizedRefFromRPC.ParentHash,
				Time:           finalizedRefFromRPC.Time,
				SequenceNumber: 0,
			}
			if finalizedRef.Number >= currentBlockNum {
				e.log.Debug("RPC finalized block is ahead of current block, keeping existing finalized",
					"finalized_from_rpc", finalizedRef.Number, "current_block", currentBlockNum)
				if e.finalizedHead != (eth.L2BlockRef{}) {
					finalizedRef = e.finalizedHead
				}
				return safeRef, finalizedRef, true, false
			}
			e.log.Debug("Got finalized block from validator via RPC", "finalized_block", finalizedRef.Number)
			return safeRef, finalizedRef, true, true
		}
		e.log.Debug("No epoch info and no elClient, skipping finalized update to avoid invalid state")
		finalizedRef = e.finalizedHead
		if finalizedRef == (eth.L2BlockRef{}) {
			finalizedRef = currentRef
		}
		return currentRef, finalizedRef, true, false
	}

	shouldUpdateSafe = currentEpoch >= 1
	shouldUpdateFinalized = currentEpoch >= 2

	if currentEpoch == 0 {
		e.log.Debug("In epoch 0, not updating safe/finalized", "current_block", currentBlockNum)
		if e.safeHead != (eth.L2BlockRef{}) {
			safeRef = e.safeHead
		}
		if e.finalizedHead != (eth.L2BlockRef{}) {
			finalizedRef = e.finalizedHead
		}
		return safeRef, finalizedRef, false, false
	}

	var safeBlockNum uint64
	if currentEpoch > 0 {
		if currentStartBlock > 0 {
			prevEpochBlockNum := currentStartBlock - 1
			_, _, prevEndBlock, prevFound := e.epochInfoGetter.GetEpochForBlock(prevEpochBlockNum)
			if prevFound {
				safeBlockNum = prevEndBlock
			} else {
				safeBlockNum = currentStartBlock - 1
			}
		} else {
			safeBlockNum = 0
		}
	} else {
		safeBlockNum = 0
	}

	var finalizedBlockNum uint64
	if currentEpoch > 1 {
		if currentStartBlock > 0 {
			prevEpochBlockNum := currentStartBlock - 1
			_, prevStartBlock, _, prevFound := e.epochInfoGetter.GetEpochForBlock(prevEpochBlockNum)
			if prevFound && prevStartBlock > 0 {
				prev2EpochBlockNum := prevStartBlock - 1
				_, _, prev2EndBlock, prev2Found := e.epochInfoGetter.GetEpochForBlock(prev2EpochBlockNum)
				if prev2Found {
					finalizedBlockNum = prev2EndBlock
				} else {
					finalizedBlockNum = prevStartBlock - 1
				}
			} else {
				epochLength := currentEndBlock - currentStartBlock + 1
				if currentStartBlock > epochLength*2 {
					finalizedBlockNum = currentStartBlock - epochLength*2
				} else {
					finalizedBlockNum = 0
				}
			}
		} else {
			finalizedBlockNum = 0
		}
	} else {
		finalizedBlockNum = 0
	}

	var err error
	if safeBlockNum < currentBlockNum {
		safeRef, err = e.engine.L2BlockRefByNumber(ctx, safeBlockNum)
		if err != nil {
			e.log.Warn("Failed to get safe block reference", "block_number", safeBlockNum, "err", err)
			if e.safeHead != (eth.L2BlockRef{}) {
				safeRef = e.safeHead
			}
		}
	} else {
		if e.safeHead != (eth.L2BlockRef{}) {
			safeRef = e.safeHead
		} else {
			safeRef = currentRef
		}
	}

	if finalizedBlockNum < currentBlockNum {
		finalizedRef, err = e.engine.L2BlockRefByNumber(ctx, finalizedBlockNum)
		if err != nil {
			e.log.Warn("Failed to get finalized block reference", "block_number", finalizedBlockNum, "err", err)
			if e.finalizedHead != (eth.L2BlockRef{}) {
				finalizedRef = e.finalizedHead
			}
		}
	} else {
		if e.finalizedHead != (eth.L2BlockRef{}) {
			finalizedRef = e.finalizedHead
		} else {
			finalizedRef = currentRef
		}
	}

	e.log.Debug("Calculated safe and finalized blocks based on epoch",
		"current_block", currentBlockNum,
		"current_epoch", currentEpoch,
		"safe_block", safeBlockNum,
		"finalized_block", finalizedBlockNum,
		"should_update_safe", shouldUpdateSafe,
		"should_update_finalized", shouldUpdateFinalized)

	return safeRef, finalizedRef, shouldUpdateSafe, shouldUpdateFinalized
}
