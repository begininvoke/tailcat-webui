package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
)

func validateShareFileMutation(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
		if mutation.Op().Is(ent.OpCreate) {
			sizeBytes, err := mutationInt64(ctx, mutation, "size_bytes")
			if err != nil {
				return nil, err
			}
			blockHashes, err := mutationStrings(ctx, mutation, "block_hashes")
			if err != nil {
				return nil, err
			}
			if len(blockHashes) != transferBlockCount(sizeBytes) {
				return nil, fmt.Errorf("share file block hash count does not match file size")
			}
		}
		return next.Mutate(ctx, mutation)
	})
}

func validateTransferJobMutation(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
		if mutation.Op().Is(ent.OpUpdate) {
			return nil, fmt.Errorf("bulk transfer job updates are not supported")
		}
		if mutation.Op().Is(ent.OpCreate | ent.OpUpdateOne) {
			state, err := transferJobState(ctx, mutation)
			if err != nil {
				return nil, err
			}
			if state.ReceivedBytes > state.TotalBytes {
				return nil, fmt.Errorf("transfer job received bytes exceed total bytes")
			}
		}
		return next.Mutate(ctx, mutation)
	})
}

func validateTransferItemMutation(next ent.Mutator) ent.Mutator {
	return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
		if mutation.Op().Is(ent.OpUpdate) {
			return nil, fmt.Errorf("bulk transfer item updates are not supported")
		}
		if mutation.Op().Is(ent.OpCreate | ent.OpUpdateOne) {
			state, err := transferItemState(ctx, mutation)
			if err != nil {
				return nil, err
			}
			if err := validateTransferBlockSize(state.BlockSize); err != nil {
				return nil, err
			}
			if len(state.BlockHashes) != transferBlockCount(state.SizeBytes) {
				return nil, fmt.Errorf("transfer item block hash count does not match file size")
			}
			if err := validateResumeProgress(state.SizeBytes, state.ReceivedBytes, state.CompletedBlocks); err != nil {
				return nil, err
			}
		}
		return next.Mutate(ctx, mutation)
	})
}

func transferJobState(ctx context.Context, mutation ent.Mutation) (TransferJobInvariantState, error) {
	if mutation.Op().Is(ent.OpUpdateOne) {
		state, err := loadTransferJobInvariantState(ctx, mutation)
		if err != nil {
			return TransferJobInvariantState{}, err
		}
		return applyTransferJobMutation(mutation, state)
	}
	totalBytes, err := mutationInt64(ctx, mutation, "total_bytes")
	if err != nil {
		return TransferJobInvariantState{}, err
	}
	receivedBytes, err := mutationInt64(ctx, mutation, "received_bytes")
	if err != nil {
		return TransferJobInvariantState{}, err
	}
	return TransferJobInvariantState{TotalBytes: totalBytes, ReceivedBytes: receivedBytes}, nil
}

func applyTransferJobMutation(mutation ent.Mutation, state TransferJobInvariantState) (TransferJobInvariantState, error) {
	if _, added := mutation.AddedField("total_bytes"); added {
		return TransferJobInvariantState{}, fmt.Errorf("additive update for total_bytes is not supported")
	}
	if _, added := mutation.AddedField("received_bytes"); added {
		return TransferJobInvariantState{}, fmt.Errorf("additive update for received_bytes is not supported")
	}
	if value, ok := mutation.Field("total_bytes"); ok {
		var valid bool
		state.TotalBytes, valid = value.(int64)
		if !valid {
			return TransferJobInvariantState{}, fmt.Errorf("unexpected type %T for total_bytes", value)
		}
	}
	if value, ok := mutation.Field("received_bytes"); ok {
		var valid bool
		state.ReceivedBytes, valid = value.(int64)
		if !valid {
			return TransferJobInvariantState{}, fmt.Errorf("unexpected type %T for received_bytes", value)
		}
	}
	return state, nil
}

func transferItemState(ctx context.Context, mutation ent.Mutation) (TransferItemInvariantState, error) {
	if mutation.Op().Is(ent.OpUpdateOne) {
		state, err := loadTransferItemInvariantState(ctx, mutation)
		if err != nil {
			return TransferItemInvariantState{}, err
		}
		return applyTransferItemMutation(mutation, state)
	}
	sizeBytes, err := mutationInt64(ctx, mutation, "size_bytes")
	if err != nil {
		return TransferItemInvariantState{}, err
	}
	blockSize, err := mutationInt64(ctx, mutation, "block_size")
	if err != nil {
		return TransferItemInvariantState{}, err
	}
	blockHashes, err := mutationStrings(ctx, mutation, "block_hashes")
	if err != nil {
		return TransferItemInvariantState{}, err
	}
	completedBlocks, err := mutationCompletedBlocks(ctx, mutation)
	if err != nil {
		return TransferItemInvariantState{}, err
	}
	receivedBytes, err := mutationInt64(ctx, mutation, "received_bytes")
	if err != nil {
		return TransferItemInvariantState{}, err
	}
	return TransferItemInvariantState{SizeBytes: sizeBytes, BlockSize: blockSize, BlockHashes: blockHashes, CompletedBlocks: completedBlocks, ReceivedBytes: receivedBytes}, nil
}

func applyTransferItemMutation(mutation ent.Mutation, state TransferItemInvariantState) (TransferItemInvariantState, error) {
	if _, added := mutation.AddedField("received_bytes"); added {
		return TransferItemInvariantState{}, fmt.Errorf("additive update for received_bytes is not supported")
	}
	if value, ok := mutation.Field("received_bytes"); ok {
		var valid bool
		state.ReceivedBytes, valid = value.(int64)
		if !valid {
			return TransferItemInvariantState{}, fmt.Errorf("unexpected type %T for received_bytes", value)
		}
	}
	if value, ok := mutation.Field("completed_blocks"); ok {
		var valid bool
		state.CompletedBlocks, valid = value.([]int)
		if !valid {
			return TransferItemInvariantState{}, fmt.Errorf("unexpected type %T for completed_blocks", value)
		}
	}
	appender, ok := mutation.(appendedCompletedBlocksMutation)
	if !ok {
		return state, nil
	}
	appended, ok := appender.AppendedCompletedBlocks()
	if !ok {
		return state, nil
	}
	if _, set := mutation.Field("completed_blocks"); set {
		return TransferItemInvariantState{}, fmt.Errorf("set and append completed blocks in one mutation are not supported")
	}
	state.CompletedBlocks = append(state.CompletedBlocks, appended...)
	return state, nil
}

func mutationInt64(ctx context.Context, mutation ent.Mutation, name string) (int64, error) {
	if _, added := mutation.AddedField(name); added {
		return 0, fmt.Errorf("additive update for %s is not supported", name)
	}
	if value, ok := mutation.Field(name); ok {
		result, ok := value.(int64)
		if !ok {
			return 0, fmt.Errorf("unexpected type %T for %s", value, name)
		}
		return result, nil
	}
	value, err := mutation.OldField(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("load existing %s: %w", name, err)
	}
	result, ok := value.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected old type %T for %s", value, name)
	}
	return result, nil
}

func mutationStrings(ctx context.Context, mutation ent.Mutation, name string) ([]string, error) {
	if value, ok := mutation.Field(name); ok {
		result, ok := value.([]string)
		if !ok {
			return nil, fmt.Errorf("unexpected type %T for %s", value, name)
		}
		return result, nil
	}
	value, err := mutation.OldField(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("load existing %s: %w", name, err)
	}
	result, ok := value.([]string)
	if !ok {
		return nil, fmt.Errorf("unexpected old type %T for %s", value, name)
	}
	return result, nil
}

type appendedCompletedBlocksMutation interface {
	AppendedCompletedBlocks() ([]int, bool)
}

func mutationCompletedBlocks(ctx context.Context, mutation ent.Mutation) ([]int, error) {
	blocks, err := mutationInts(ctx, mutation, "completed_blocks")
	if err != nil {
		return nil, err
	}
	appender, ok := mutation.(appendedCompletedBlocksMutation)
	if !ok {
		return blocks, nil
	}
	appended, ok := appender.AppendedCompletedBlocks()
	if !ok {
		return blocks, nil
	}
	if _, set := mutation.Field("completed_blocks"); set {
		return nil, fmt.Errorf("set and append completed blocks in one mutation are not supported")
	}
	return append(blocks, appended...), nil
}

func mutationInts(ctx context.Context, mutation ent.Mutation, name string) ([]int, error) {
	if value, ok := mutation.Field(name); ok {
		result, ok := value.([]int)
		if !ok {
			return nil, fmt.Errorf("unexpected type %T for %s", value, name)
		}
		return result, nil
	}
	value, err := mutation.OldField(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("load existing %s: %w", name, err)
	}
	result, ok := value.([]int)
	if !ok {
		return nil, fmt.Errorf("unexpected old type %T for %s", value, name)
	}
	return result, nil
}

func transferBlockCount(sizeBytes int64) int {
	if sizeBytes == 0 {
		return 0
	}
	count := int(sizeBytes / transferBlockSizeBytes)
	if sizeBytes%transferBlockSizeBytes != 0 {
		count++
	}
	return count
}

func validateResumeProgress(sizeBytes, receivedBytes int64, completedBlocks []int) error {
	if receivedBytes > sizeBytes {
		return fmt.Errorf("transfer item received bytes exceed file size")
	}
	blockCount := transferBlockCount(sizeBytes)
	seen := make(map[int]struct{}, len(completedBlocks))
	var completeBytes int64
	for _, block := range completedBlocks {
		if block < 0 || block >= blockCount {
			return fmt.Errorf("transfer item completed block index is out of range")
		}
		if _, exists := seen[block]; exists {
			return fmt.Errorf("transfer item completed block index is duplicated")
		}
		seen[block] = struct{}{}
		blockBytes := transferBlockSizeBytes
		if block == blockCount-1 && sizeBytes%transferBlockSizeBytes != 0 {
			blockBytes = sizeBytes % transferBlockSizeBytes
		}
		completeBytes += blockBytes
	}
	if receivedBytes < completeBytes {
		return fmt.Errorf("transfer item received bytes omit completed blocks")
	}
	return nil
}
