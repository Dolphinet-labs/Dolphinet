package node

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/p2p"
	"github.com/dolphinnet-labs/dolphinnet/dn-node/rollup"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/eth"
)

var (
	// UnsafeBlockSignerAddressSystemConfigStorageSlot is the storage slot identifier of the unsafeBlockSigner
	// `address` storage value in the SystemConfig L1 contract. Computed as `keccak256("systemconfig.unsafeblocksigner")`
	UnsafeBlockSignerAddressSystemConfigStorageSlot = common.HexToHash("0x65a7ed542fb37fe237fdfbdd70b31598523fe5b32879e307bae27a0bd9581c08")

	// RequiredProtocolVersionStorageSlot is the storage slot that the required protocol version is stored at.
	// Computed as: `bytes32(uint256(keccak256("protocolversion.required")) - 1)`
	RequiredProtocolVersionStorageSlot = common.HexToHash("0x4aaefe95bd84fd3f32700cf3b7566bc944b73138e41958b5785826df2aecace0")

	// RecommendedProtocolVersionStorageSlot is the storage slot that the recommended protocol version is stored at.
	// Computed as: `bytes32(uint256(keccak256("protocolversion.recommended")) - 1)`
	RecommendedProtocolVersionStorageSlot = common.HexToHash("0xe314dfc40f0025322aacc0ba8ef420b62fb3b702cf01e0cdf3d829117ac2ff1a")
)

type RuntimeCfgL1Source interface {
	ReadStorageAt(ctx context.Context, address common.Address, storageSlot common.Hash, blockHash common.Hash) (common.Hash, error)
}

type ReadonlyRuntimeConfig interface {
	P2PSequencerAddress() common.Address
	RequiredProtocolVersion() params.ProtocolVersion
	RecommendedProtocolVersion() params.ProtocolVersion
}

// RuntimeConfig maintains runtime-configurable options.
// These options are loaded based on initial loading + updates for every subsequent L1 block.
// Only the *latest* values are maintained however, the runtime config has no concept of chain history,
// does not require any archive data, and may be out of sync with the rollup derivation process.
type RuntimeConfig struct {
	mu sync.RWMutex

	log log.Logger

	rollupCfg *rollup.Config

	// l1Ref is the current source of the data,
	// if this is invalidated with a reorg the data will have to be reloaded.
	l1Ref eth.L1BlockRef

	runtimeConfigData
}

// runtimeConfigData is a flat bundle of configurable data, easy and light to copy around.
type runtimeConfigData struct {
	p2pBlockSignerAddr common.Address
	// p2pAllowedSequencerAddrs is a list of all allowed sequencer addresses for PoS mode.
	// In PoS mode, multiple validators can produce blocks, so we need to accept blocks from all of them.
	p2pAllowedSequencerAddrs []common.Address

	// superchain protocol version signals
	recommended params.ProtocolVersion
	required    params.ProtocolVersion
}

var _ p2p.GossipRuntimeConfig = (*RuntimeConfig)(nil)

func NewRuntimeConfig(log log.Logger, rollupCfg *rollup.Config) *RuntimeConfig {
	return &RuntimeConfig{
		log:       log,
		rollupCfg: rollupCfg,
	}
}

func (r *RuntimeConfig) P2PSequencerAddress() common.Address {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.p2pBlockSignerAddr
}

func (r *RuntimeConfig) P2PAllowedSequencerAddresses() []common.Address {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Return a copy to prevent external modifications
	if len(r.p2pAllowedSequencerAddrs) == 0 {
		// Fallback to single address for backward compatibility
		if r.p2pBlockSignerAddr != (common.Address{}) {
			return []common.Address{r.p2pBlockSignerAddr}
		}
		return nil
	}
	result := make([]common.Address, len(r.p2pAllowedSequencerAddrs))
	copy(result, r.p2pAllowedSequencerAddrs)
	return result
}

func (r *RuntimeConfig) RequiredProtocolVersion() params.ProtocolVersion {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.required
}

func (r *RuntimeConfig) RecommendedProtocolVersion() params.ProtocolVersion {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.recommended
}

// Load resets the runtime configuration by fetching the latest config data from L1 at the given L1 block.
// Load is safe to call concurrently, but will lock the runtime configuration modifications only,
// and will thus not block other Load calls with possibly alternative L1 block views.
func (r *RuntimeConfig) Load(ctx context.Context, p2pSignerAddress common.Address) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.p2pBlockSignerAddr = p2pSignerAddress
	if p2pSignerAddress != (common.Address{}) {
		r.p2pAllowedSequencerAddrs = []common.Address{p2pSignerAddress}
	} else {
		r.p2pAllowedSequencerAddrs = []common.Address{}
	}
	r.required = params.ProtocolVersion(common.HexToHash("0x1"))
	r.recommended = params.ProtocolVersion(common.HexToHash("0x1"))
	r.log.Info("loaded new runtime config values!", "p2p_seq_address", r.p2pBlockSignerAddr)
	return nil
}

// UpdateAllowedSequencerAddresses updates the list of allowed sequencer addresses.
// This is called when receiving EpochSchedule from the manager, which contains all validator addresses.
func (r *RuntimeConfig) UpdateAllowedSequencerAddresses(addresses []common.Address) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.p2pAllowedSequencerAddrs = make([]common.Address, len(addresses))
	copy(r.p2pAllowedSequencerAddrs, addresses)
	r.log.Info("updated allowed sequencer addresses", "count", len(addresses))
}
