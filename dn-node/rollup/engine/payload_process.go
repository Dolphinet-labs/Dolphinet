package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/eth"
)

type PayloadProcessEvent struct {
	// if payload should be promoted to (local) safe (must also be pending safe, see DerivedFrom)
	Concluding bool
	// payload is promoted to pending-safe if non-zero
	BuildStarted time.Time

	Envelope *eth.ExecutionPayloadEnvelope
	Ref      eth.L2BlockRef
}

func (ev PayloadProcessEvent) String() string {
	return "payload-process"
}

func (eq *EngDeriver) onPayloadProcess(ev PayloadProcessEvent) {
	ctx, cancel := context.WithTimeout(eq.ctx, payloadProcessTimeout)
	defer cancel()

	insertStart := time.Now()
	status, err := eq.ec.engine.NewPayload(ctx,
		ev.Envelope.ExecutionPayload, ev.Envelope.ParentBeaconBlockRoot)
	if err != nil {
		eq.emitter.Emit(rollup.EngineTemporaryErrorEvent{
			Err: fmt.Errorf("failed to insert execution payload: %w", err),
		})
		return
	}
	switch status.Status {
	case eth.ExecutionInvalid, eth.ExecutionInvalidBlockHash:
		eq.emitter.Emit(PayloadInvalidEvent{
			Envelope: ev.Envelope,
			Err:      eth.NewPayloadErr(ev.Envelope.ExecutionPayload, status),
		})
		return
	case eth.ExecutionValid:
		eq.emitter.Emit(PayloadSuccessEvent{
			Concluding:    ev.Concluding,
			BuildStarted:  ev.BuildStarted,
			InsertStarted: insertStart,
			Envelope:      ev.Envelope,
			Ref:           ev.Ref,
		})
		return
	case eth.ExecutionSyncing:
		// Engine is syncing, likely because parent blocks are missing
		// Don't emit an error immediately - wait for missing blocks to arrive via P2P
		// The checkForGapInUnsafeQueue will request missing blocks, and once they arrive,
		// we can retry processing this payload
		eq.log.Debug("Engine is syncing, waiting for missing parent blocks",
			"payload", ev.Envelope.ExecutionPayload.ID(),
			"parent", ev.Envelope.ExecutionPayload.ParentID())
		// Don't emit an error - just return and wait for the next forkchoice update
		// The payload will remain in the queue and be retried once parent blocks arrive
		return
	default:
		eq.emitter.Emit(rollup.EngineTemporaryErrorEvent{
			Err: eth.NewPayloadErr(ev.Envelope.ExecutionPayload, status),
		})
		return
	}
}
