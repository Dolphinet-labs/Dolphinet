package clsync

import (
	"sync"

	"github.com/ethereum/go-ethereum/log"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/derive"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/engine"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup/event"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/eth"
)

// Max memory used for buffering unsafe payloads
const maxUnsafePayloadsMemory = 500 * 1024 * 1024

type Metrics interface {
	RecordUnsafePayloadsBuffer(length uint64, memSize uint64, next eth.BlockID)
}

// CLSync holds on to a queue of received unsafe payloads,
// and tries to apply them to the tip of the chain when requested to.
type CLSync struct {
	log     log.Logger
	cfg     *rollup.Config
	metrics Metrics

	emitter event.Emitter

	mu sync.Mutex

	unsafePayloads *PayloadsQueue // queue of unsafe payloads, ordered by ascending block number, may have gaps and duplicates
}

func NewCLSync(log log.Logger, cfg *rollup.Config, metrics Metrics) *CLSync {
	return &CLSync{
		log:            log,
		cfg:            cfg,
		metrics:        metrics,
		unsafePayloads: NewPayloadsQueue(log, maxUnsafePayloadsMemory, payloadMemSize),
	}
}

func (eq *CLSync) AttachEmitter(em event.Emitter) {
	eq.emitter = em
}

// LowestQueuedUnsafeBlock retrieves the first queued-up core unsafe payload, or a zeroed reference if there is none.
func (eq *CLSync) LowestQueuedUnsafeBlock() eth.L2BlockRef {
	payload := eq.unsafePayloads.Peek()
	if payload == nil {
		return eth.L2BlockRef{}
	}
	ref, err := derive.PayloadToBlockRef(eq.cfg, payload.ExecutionPayload)
	if err != nil {
		return eth.L2BlockRef{}
	}
	return ref
}

type ReceivedUnsafePayloadEvent struct {
	Envelope *eth.ExecutionPayloadEnvelope
}

func (ev ReceivedUnsafePayloadEvent) String() string {
	return "received-unsafe-payload"
}

func (eq *CLSync) OnEvent(ev event.Event) bool {
	// Events may be concurrent in the future. Prevent unsafe concurrent modifications to the payloads queue.
	eq.mu.Lock()
	defer eq.mu.Unlock()

	switch x := ev.(type) {
	case engine.PayloadInvalidEvent:
		eq.onInvalidPayload(x)
	case engine.ForkchoiceUpdateEvent:
		eq.onForkchoiceUpdate(x)
	case ReceivedUnsafePayloadEvent:
		eq.onUnsafePayload(x)
	default:
		return false
	}
	return true
}

// onInvalidPayload checks if the first next-up payload matches the invalid payload.
// If so, the payload is dropped, to give the next payloads a try.
func (eq *CLSync) onInvalidPayload(x engine.PayloadInvalidEvent) {
	eq.log.Debug("CL sync received invalid-payload report", "id", x.Envelope.ExecutionPayload.ID())

	block := x.Envelope.ExecutionPayload
	if peek := eq.unsafePayloads.Peek(); peek != nil &&
		block.BlockHash == peek.ExecutionPayload.BlockHash {
		eq.log.Warn("Dropping invalid unsafe payload",
			"hash", block.BlockHash, "number", uint64(block.BlockNumber),
			"timestamp", uint64(block.Timestamp))
		eq.unsafePayloads.Pop()
	}
}

// onForkchoiceUpdate peeks at the next applicable unsafe payload, if any,
// to apply on top of the received forkchoice pre-state.
// The payload is held on to until the forkchoice changes (success case) or the payload is reported to be invalid.
func (eq *CLSync) onForkchoiceUpdate(x engine.ForkchoiceUpdateEvent) {
	eq.log.Debug("CL sync received forkchoice update",
		"unsafe", x.UnsafeL2Head, "safe", x.SafeL2Head, "finalized", x.FinalizedL2Head)

	for {
		pop, abort := eq.fromQueue(x)
		if abort {
			return
		}
		if pop {
			eq.unsafePayloads.Pop()
		} else {
			break
		}
	}

	firstEnvelope := eq.unsafePayloads.Peek()

	// We don't pop from the queue. If there is a temporary error then we can retry.
	// Upon next forkchoice update or invalid-payload event we can remove it from the queue.
	eq.emitter.Emit(engine.ProcessUnsafePayloadEvent{Envelope: firstEnvelope})
}

// fromQueue determines what to do with the tip of the payloads-queue, given the forkchoice pre-state.
// If abort, there is nothing to process (either due to empty queue, or unsuitable tip).
// If pop, the tip should be dropped, and processing can repeat from there.
// If not abort or pop, the tip is ready to process.
func (eq *CLSync) fromQueue(x engine.ForkchoiceUpdateEvent) (pop bool, abort bool) {
	if eq.unsafePayloads.Len() == 0 {
		return false, true
	}
	firstEnvelope := eq.unsafePayloads.Peek()
	first := firstEnvelope.ExecutionPayload

	if first.BlockHash == x.UnsafeL2Head.Hash {
		eq.log.Debug("successfully processed payload, removing it from the payloads queue now")
		return true, false
	}

	if uint64(first.BlockNumber) <= x.SafeL2Head.Number {
		eq.log.Info("skipping unsafe payload, since it is older than safe head", "safe", x.SafeL2Head.ID(), "unsafe", x.UnsafeL2Head.ID(), "unsafe_payload", first.ID())
		return true, false
	}
	if uint64(first.BlockNumber) <= x.UnsafeL2Head.Number {
		eq.log.Info("skipping unsafe payload, since it is older than unsafe head", "unsafe", x.UnsafeL2Head.ID(), "unsafe_payload", first.ID())
		return true, false
	}

	// Ensure that the unsafe payload builds upon the current unsafe head
	if first.ParentHash != x.UnsafeL2Head.Hash {
		if uint64(first.BlockNumber) > x.UnsafeL2Head.Number {
			// The block is ahead of our chain, but parent hash doesn't match
			// In PoS mode, this likely means we're missing parent blocks from validator1
			// We should try to process this block anyway - the engine will handle it:
			// - If parent is missing, engine returns ExecutionSyncing, and checkForGapInUnsafeQueue will request missing blocks
			// - If it's a fork, engine will handle the reorg
			eq.log.Info("unsafe payload parent doesn't match current unsafe head, attempting to process anyway",
				"safe", x.SafeL2Head.ID(),
				"unsafe", x.UnsafeL2Head.ID(),
				"unsafe_payload", first.ID(),
				"payload_parent", first.ParentID(),
				"current_unsafe_hash", x.UnsafeL2Head.Hash,
				"gap_size", uint64(first.BlockNumber)-x.UnsafeL2Head.Number-1)
			// Don't skip, let the engine try to process it
			// The engine's NewPayload will handle missing parents or reorgs as needed
			return false, false
		}
		return false, true // rollup-node should try something different if it cannot process the first unsafe payload
	}

	return false, false
}

// AddUnsafePayload schedules an execution payload to be processed, ahead of deriving it from L1.
func (eq *CLSync) onUnsafePayload(x ReceivedUnsafePayloadEvent) {
	eq.log.Debug("CL sync received payload", "payload", x.Envelope.ExecutionPayload.ID())
	envelope := x.Envelope
	if envelope == nil {
		eq.log.Warn("cannot add nil unsafe payload")
		return
	}

	if err := eq.unsafePayloads.Push(envelope); err != nil {
		eq.log.Warn("Could not add unsafe payload", "id", envelope.ExecutionPayload.ID(), "timestamp", uint64(envelope.ExecutionPayload.Timestamp), "err", err)
		return
	}
	p := eq.unsafePayloads.Peek()
	eq.metrics.RecordUnsafePayloadsBuffer(uint64(eq.unsafePayloads.Len()), eq.unsafePayloads.MemSize(), p.ExecutionPayload.ID())
	eq.log.Trace("Next unsafe payload to process", "next", p.ExecutionPayload.ID(), "timestamp", uint64(p.ExecutionPayload.Timestamp))

	// request forkchoice signal, so we can process the payload maybe
	eq.emitter.Emit(engine.ForkchoiceRequestEvent{})
}
