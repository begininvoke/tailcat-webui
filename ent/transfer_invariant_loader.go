package ent

import (
	"context"
	"fmt"

	entgo "entgo.io/ent"
	"github.com/ca-x/tailcat-webui/ent/schema"
)

func init() {
	schema.RegisterTransferInvariantLoaders(loadTransferJobInvariantState, loadTransferItemInvariantState)
}

func loadTransferJobInvariantState(ctx context.Context, mutation entgo.Mutation) (schema.TransferJobInvariantState, error) {
	jobMutation, ok := mutation.(*TransferJobMutation)
	if !ok {
		return schema.TransferJobInvariantState{}, fmt.Errorf("unexpected transfer job mutation %T", mutation)
	}
	id, ok := jobMutation.ID()
	if !ok {
		return schema.TransferJobInvariantState{}, fmt.Errorf("transfer job update is missing its ID")
	}
	job, err := jobMutation.Client().TransferJob.Get(ctx, id)
	if err != nil {
		return schema.TransferJobInvariantState{}, fmt.Errorf("load current transfer job: %w", err)
	}
	return schema.TransferJobInvariantState{TotalBytes: job.TotalBytes, ReceivedBytes: job.ReceivedBytes}, nil
}

func loadTransferItemInvariantState(ctx context.Context, mutation entgo.Mutation) (schema.TransferItemInvariantState, error) {
	itemMutation, ok := mutation.(*TransferItemMutation)
	if !ok {
		return schema.TransferItemInvariantState{}, fmt.Errorf("unexpected transfer item mutation %T", mutation)
	}
	id, ok := itemMutation.ID()
	if !ok {
		return schema.TransferItemInvariantState{}, fmt.Errorf("transfer item update is missing its ID")
	}
	item, err := itemMutation.Client().TransferItem.Get(ctx, id)
	if err != nil {
		return schema.TransferItemInvariantState{}, fmt.Errorf("load current transfer item: %w", err)
	}
	return schema.TransferItemInvariantState{
		SizeBytes:       item.SizeBytes,
		BlockSize:       item.BlockSize,
		BlockHashes:     item.BlockHashes,
		CompletedBlocks: item.CompletedBlocks,
		ReceivedBytes:   item.ReceivedBytes,
	}, nil
}
