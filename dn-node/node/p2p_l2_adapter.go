package node

import (
	"context"

	"github.com/dolphinnet-labs/dolphinnet/dn-node/p2p"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/eth"
	"github.com/dolphinnet-labs/dolphinnet/dn-service/sources"
)

// p2pL2ChainAdapter adapts sources.EngineClient to p2p.L2Chain by adding L2Head for open-ended P2P sync.
type p2pL2ChainAdapter struct {
	*sources.EngineClient
}

var _ p2p.L2Chain = (*p2pL2ChainAdapter)(nil)

// L2Head returns the current unsafe L2 head so P2P sync clients can use it as a sync target.
func (a *p2pL2ChainAdapter) L2Head(ctx context.Context) (eth.L2BlockRef, error) {
	return a.L2BlockRefByLabel(ctx, eth.Unsafe)
}
