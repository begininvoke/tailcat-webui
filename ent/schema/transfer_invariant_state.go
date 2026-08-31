package schema

import (
	"context"
	"errors"

	"entgo.io/ent"
)

// TransferJobInvariantState is the authoritative state needed to validate a
// receive-job update. It is loaded by the generated Ent client, not inferred
// from a possibly partial selected entity.
type TransferJobInvariantState struct {
	TotalBytes    int64
	ReceivedBytes int64
}

// TransferItemInvariantState is the authoritative manifest and resume state
// needed to validate an item update.
type TransferItemInvariantState struct {
	SizeBytes       int64
	BlockSize       int64
	BlockHashes     []string
	CompletedBlocks []int
	ReceivedBytes   int64
}

var (
	transferJobInvariantLoader  func(context.Context, ent.Mutation) (TransferJobInvariantState, error)
	transferItemInvariantLoader func(context.Context, ent.Mutation) (TransferItemInvariantState, error)
)

// RegisterTransferInvariantLoaders is called during root Ent package
// initialization. Keeping the loader on the schema hook avoids a schema-to-
// generated-client import cycle while preserving transactional reads.
func RegisterTransferInvariantLoaders(
	jobLoader func(context.Context, ent.Mutation) (TransferJobInvariantState, error),
	itemLoader func(context.Context, ent.Mutation) (TransferItemInvariantState, error),
) {
	transferJobInvariantLoader = jobLoader
	transferItemInvariantLoader = itemLoader
}

func loadTransferJobInvariantState(ctx context.Context, mutation ent.Mutation) (TransferJobInvariantState, error) {
	if transferJobInvariantLoader == nil {
		return TransferJobInvariantState{}, errors.New("transfer job invariant loader is unavailable")
	}
	return transferJobInvariantLoader(ctx, mutation)
}

func loadTransferItemInvariantState(ctx context.Context, mutation ent.Mutation) (TransferItemInvariantState, error) {
	if transferItemInvariantLoader == nil {
		return TransferItemInvariantState{}, errors.New("transfer item invariant loader is unavailable")
	}
	return transferItemInvariantLoader(ctx, mutation)
}
