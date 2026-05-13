// Package sync is responsible for reconciling L1 and core.
//
// The Ethereum chain is a DAG of blocks with the root block being the genesis block. At any given
// time, the head (or tip) of the chain can change if an offshoot/branch of the chain has a higher
// total difficulty. This is known as a re-organization of the canonical chain. Each block points to
// a parent block and the node is responsible for deciding which block is the head and thus the
// mapping from block number to canonical block.
//
// The Optimism (core) chain has similar properties, but also retains references to the Ethereum (L1)
// chain. Each core block retains a reference to an L1 block (its "L1 origin", i.e. L1 block
// associated with the epoch that the core block belongs to) and to its parent core block. The core chain
// node must satisfy the following validity rules:
//
//  1. l2block.number == l2block.l2parent.block.number + 1
//  2. l2block.l1Origin.number >= l2block.l2parent.l1Origin.number
//  3. l2block.l1Origin is in the canonical chain on L1
//  4. l1_rollup_genesis is an ancestor of l2block.l1Origin
//
// During normal operation, both the L1 and core canonical chains can change, due to a re-organisation
// or due to an extension (new L1 or core block).
//
// In particular, in the case of L1 extension, the core unsafe head will generally remain the same,
// but in the case of an L1 re-org, we need to search for the new safe and unsafe core block.
package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/eth"
)

type L1Chain interface {
	L1BlockRefByLabel(ctx context.Context, label eth.BlockLabel) (eth.L1BlockRef, error)
	L1BlockRefByNumber(ctx context.Context, number uint64) (eth.L1BlockRef, error)
	L1BlockRefByHash(ctx context.Context, hash common.Hash) (eth.L1BlockRef, error)
}

type L2Chain interface {
	L2BlockRefByHash(ctx context.Context, l2Hash common.Hash) (eth.L2BlockRef, error)
	L2BlockRefByLabel(ctx context.Context, label eth.BlockLabel) (eth.L2BlockRef, error)
}

var ReorgFinalizedErr = errors.New("cannot reorg finalized block")
var WrongChainErr = errors.New("wrong chain")
var TooDeepReorgErr = errors.New("reorg is too deep")

const MaxReorgSeqWindows = 5

// RecoverMinSeqWindows is the number of sequence windows
// between the unsafe head L1 origin, and the finalized block, while finality is still at genesis,
// that need to elapse to heuristically recover from a missing forkchoice state.
// Note that in healthy node circumstances finality should have been forced a long time ago,
// since blocks are force-inserted after a full sequence window.
const RecoverMinSeqWindows = 14

type FindHeadsResult struct {
	Unsafe    eth.L2BlockRef
	Safe      eth.L2BlockRef
	Finalized eth.L2BlockRef
}

// currentHeads returns the current finalized, safe and unsafe heads of the execution engine.
// If nothing has been marked finalized yet, the finalized head defaults to the genesis block.
// If nothing has been marked safe yet, the safe head defaults to the finalized block.
func currentHeads(ctx context.Context, cfg *rollup.Config, l2 L2Chain) (*FindHeadsResult, error) {
	finalized, err := l2.L2BlockRefByLabel(ctx, eth.Finalized)
	if errors.Is(err, ethereum.NotFound) {
		// default to genesis if we have not finalized anything before.
		finalized, err = l2.L2BlockRefByHash(ctx, cfg.Genesis.L2.Hash)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find the finalized core block: %w", err)
	}

	safe, err := l2.L2BlockRefByLabel(ctx, eth.Safe)
	if errors.Is(err, ethereum.NotFound) {
		safe = finalized
	} else if err != nil {
		return nil, fmt.Errorf("failed to find the safe core block: %w", err)
	}

	unsafe, err := l2.L2BlockRefByLabel(ctx, eth.Unsafe)
	if err != nil {
		return nil, fmt.Errorf("failed to find the core head block: %w", err)
	}
	return &FindHeadsResult{
		Unsafe:    unsafe,
		Safe:      safe,
		Finalized: finalized,
	}, nil
}

// FindL2Heads walks back from `start` (the previous unsafe core block) and finds
// the finalized, unsafe and safe core blocks.
//
//   - The *unsafe core block*: This is the highest core block whose L1 origin is a *plausible*
//     extension of the canonical L1 chain (as known to the dn-node).
//   - The *safe core block*: This is the highest core block whose epoch's sequencing window is
//     complete within the canonical L1 chain (as known to the dn-node).
//   - The *finalized core block*: This is the core block which is known to be fully derived from
//     finalized L1 block data.
//
// Plausible: meaning that the blockhash of the core block's L1 origin
// (as reported in the L1 Attributes deposit within the core block) is not canonical at another height in the L1 chain,
// and the same holds for all its ancestors.
func FindL2Heads(ctx context.Context, cfg *rollup.Config, l2 L2Chain, lgr log.Logger, syncCfg *Config) (result *FindHeadsResult, err error) {
	// Fetch current core forkchoice state
	result, err = currentHeads(ctx, cfg, l2)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch current core forkchoice state: %w", err)
	}

	lgr.Info("Loaded current core heads", "unsafe", result.Unsafe, "safe", result.Safe, "finalized", result.Finalized,
		"unsafe_origin", result.Unsafe.L1Origin, "safe_origin", result.Safe.L1Origin)

	// Check if the execution engine completed sync, but left the forkchoice finalized & safe heads at genesis.
	// This is the equivalent of the "syncStatusFinishedELButNotFinalized" post-processing in the engine controller.
	if result.Finalized.Hash == cfg.Genesis.L2.Hash &&
		result.Safe.Hash == cfg.Genesis.L2.Hash &&
		result.Unsafe.Number > cfg.Genesis.L2.Number {
		lgr.Warn("Attempting recovery from sync state without finality.", "head", result.Unsafe)
		return &FindHeadsResult{Unsafe: result.Unsafe, Safe: result.Unsafe, Finalized: result.Unsafe}, nil
	}

	// Check if the execution engine corrupted, and forkchoice is ahead of the remaining chain:
	// in this case we must go back to the prior head, to reprocess the pruned finalized/safe data.
	if result.Unsafe.Number < result.Finalized.Number || result.Unsafe.Number < result.Safe.Number {
		lgr.Error("Unsafe head is behind known finalized/safe blocks, execution-engine chain must have been rewound without forkchoice update. Attempting recovery now.",
			"unsafe_head", result.Unsafe, "safe_head", result.Safe, "finalized_head", result.Finalized)
		return &FindHeadsResult{Unsafe: result.Unsafe, Safe: result.Unsafe, Finalized: result.Unsafe}, nil
	}

	// Remember original unsafe block to determine reorg depth
	prevUnsafe := result.Unsafe

	// When EL's finalized label has lagged far behind unsafe (e.g. sequencer never updated FCU
	// safe/finalized), the parent-walk below can require millions of L2BlockRefByHash RPCs and block
	// engine reset indefinitely — while reset is stuck the sequencer cannot seal blocks to fix FCU.
	// This fast-path matches the loop's terminal state at the finalized block number and breaks that cycle.
	const maxFindHeadsParentWalk = 65536
	if prevUnsafe.Number > result.Finalized.Number {
		gap := prevUnsafe.Number - result.Finalized.Number
		if gap > maxFindHeadsParentWalk {
			lgr.Warn("FindL2Heads: unsafe-finalized gap too large to walk parents; using EL unsafe + finalized for reset (upgrade node so FCU keeps safe/finalized near head)",
				"unsafe", prevUnsafe.Number, "safe_label", result.Safe.Number, "finalized_label", result.Finalized.Number, "gap", gap, "walk_limit", maxFindHeadsParentWalk)
			return &FindHeadsResult{
				Unsafe:    prevUnsafe,
				Safe:      result.Finalized,
				Finalized: result.Finalized,
			}, nil
		}
	}

	// Current core block.
	n := result.Unsafe

	var highestL2WithCanonicalL1Origin eth.L2BlockRef // the highest core block with confirmed canonical L1 origin
	var ahead bool                                    // when "n", the core block, has a L1 origin that is not visible in our L1 chain source yet

	ready := false // when we found the block after the safe head, and we just need to return the parent block.
	// Each loop iteration we traverse further from the unsafe head towards the finalized head.
	// Once we pass the previous safe head and we have seen enough canonical L1 origins to fill a sequence window worth of data,
	// then we return the last core block of the epoch before that as safe head.
	// Each loop iteration we traverse a single core block, and we check if the L1 origins are consistent.
	for {
		// Don't walk past genesis. If we were at the core genesis, but could not find its L1 origin,
		// the core chain is building on the wrong L1 branch.
		if n.Number == cfg.Genesis.L2.Number {
			// Check core traversal against core Genesis data, to make sure the engine is on the correct chain, instead of attempting sync with different core destination.
			if n.Hash != cfg.Genesis.L2.Hash {
				return nil, fmt.Errorf("%w core: genesis: %s, got %s", WrongChainErr, cfg.Genesis.L2, n)
			}
		}
		// Check core traversal against finalized data
		if (n.Number == result.Finalized.Number) && (n.Hash != result.Finalized.Hash) {
			return nil, fmt.Errorf("%w: finalized %s, got: %s", ReorgFinalizedErr, result.Finalized, n)
		}

		// If we don't have a usable unsafe head, then set it
		if result.Unsafe == (eth.L2BlockRef{}) {
			result.Unsafe = n
			// Check we are not reorging core incredibly deep
			if n.L1Origin.Number+(MaxReorgSeqWindows*cfg.SyncLookback()) < prevUnsafe.L1Origin.Number {
				// If the reorg depth is too large, something is fishy.
				// This can legitimately happen if L1 goes down for a while. But in that case,
				// restarting the core node with a bigger configured MaxReorgDepth is an acceptable
				// stopgap solution.
				return nil, fmt.Errorf("%w: traversed back to core block %s, but too deep compared to previous unsafe block %s", TooDeepReorgErr, n, prevUnsafe)
			}
		}

		if ahead {
			// discard previous candidate
			highestL2WithCanonicalL1Origin = eth.L2BlockRef{}
			// keep the unsafe head if we can't tell if its L1 origin is canonical or not yet.
		} else {
			// L1 origin neither ahead of L1 head nor canonical, discard previous candidate and keep looking.
			result.Unsafe = eth.L2BlockRef{}
			highestL2WithCanonicalL1Origin = eth.L2BlockRef{}
		}

		// If the core block is at least as old as the previous safe head, and we have seen at least a full sequence window worth of L1 blocks to confirm
		if n.Number <= result.Safe.Number && n.L1Origin.Number+cfg.SyncLookback() < highestL2WithCanonicalL1Origin.L1Origin.Number && n.SequenceNumber == 0 {
			ready = true
		}

		// Don't traverse further than the finalized head to find a safe head
		if n.Number == result.Finalized.Number {
			lgr.Info("Hit finalized core head, returning immediately", "unsafe", result.Unsafe, "safe", result.Safe,
				"finalized", result.Finalized, "unsafe_origin", result.Unsafe.L1Origin, "safe_origin", result.Safe.L1Origin)
			result.Safe = n
			return result, nil
		}

		if syncCfg.SkipSyncStartCheck && highestL2WithCanonicalL1Origin.Hash == n.Hash {
			lgr.Info("Found highest core block with canonical L1 origin. Skip further sanity check and jump to the safe head")
			n = result.Safe
			continue
		}
		// Pull core parent for next iteration
		parent, err := l2.L2BlockRefByHash(ctx, n.ParentHash)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch core block by hash %v: %w", n.ParentHash, err)
		}
		n = parent

		// once we found the block at seq nr 0 that is more than a full seq window behind the common chain post-reorg, then use the parent block as safe head.
		if ready {
			result.Safe = n
			return result, nil
		}
	}
}
