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
			if mutation.Op().Is(ent.OpUpdateOne) {
				if err := requireTransferJobUpdateMarkers(ctx, mutation); err != nil {
					return nil, err
				}
			}
			totalBytes, err := mutationInt64(ctx, mutation, "total_bytes")
			if err != nil {
				return nil, err
			}
			receivedBytes, err := mutationInt64(ctx, mutation, "received_bytes")
			if err != nil {
				return nil, err
			}
			if receivedBytes > totalBytes {
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
			if mutation.Op().Is(ent.OpUpdateOne) {
				if err := requireTransferItemUpdateMarkers(ctx, mutation); err != nil {
					return nil, err
				}
			}
			sizeBytes, err := mutationInt64(ctx, mutation, "size_bytes")
			if err != nil {
				return nil, err
			}
			receivedBytes, err := mutationInt64(ctx, mutation, "received_bytes")
			if err != nil {
				return nil, err
			}
			blockHashes, err := mutationStrings(ctx, mutation, "block_hashes")
			if err != nil {
				return nil, err
			}
			if len(blockHashes) != transferBlockCount(sizeBytes) {
				return nil, fmt.Errorf("transfer item block hash count does not match file size")
			}
			completedBlocks, err := mutationCompletedBlocks(ctx, mutation)
			if err != nil {
				return nil, err
			}
			if err := validateResumeProgress(sizeBytes, receivedBytes, completedBlocks); err != nil {
				return nil, err
			}
		}
		return next.Mutate(ctx, mutation)
	})
}

func requireTransferJobUpdateMarkers(ctx context.Context, mutation ent.Mutation) error {
	for _, name := range []string{"user_id", "client_id", "remote_share_id"} {
		value, err := mutationOldString(ctx, mutation, name)
		if err != nil {
			return err
		}
		if err := validateUUIDv7(value); err != nil {
			return fmt.Errorf("incomplete transfer job entity for update: %w", err)
		}
	}
	capability, err := mutationOldBytes(ctx, mutation, "remote_capability_cipher")
	if err != nil {
		return err
	}
	if len(capability) == 0 {
		return fmt.Errorf("incomplete transfer job entity for update")
	}
	return nil
}

func requireTransferItemUpdateMarkers(ctx context.Context, mutation ent.Mutation) error {
	for _, name := range []string{"user_id", "job_id", "remote_file_id"} {
		value, err := mutationOldString(ctx, mutation, name)
		if err != nil {
			return err
		}
		if err := validateUUIDv7(value); err != nil {
			return fmt.Errorf("incomplete transfer item entity for update: %w", err)
		}
	}
	storageName, err := mutationOldString(ctx, mutation, "storage_name")
	if err != nil {
		return err
	}
	if err := validateStorageName(storageName); err != nil {
		return fmt.Errorf("incomplete transfer item entity for update: %w", err)
	}
	virtualPath, err := mutationOldString(ctx, mutation, "virtual_path")
	if err != nil {
		return err
	}
	if err := validateVirtualPath(virtualPath); err != nil {
		return fmt.Errorf("incomplete transfer item entity for update: %w", err)
	}
	blake3, err := mutationOldString(ctx, mutation, "blake3")
	if err != nil {
		return err
	}
	if err := validateBLAKE3(blake3); err != nil {
		return fmt.Errorf("incomplete transfer item entity for update: %w", err)
	}
	blockSize, err := mutationOldInt64(ctx, mutation, "block_size")
	if err != nil {
		return err
	}
	if err := validateTransferBlockSize(blockSize); err != nil {
		return fmt.Errorf("incomplete transfer item entity for update: %w", err)
	}
	return nil
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

func mutationOldInt64(ctx context.Context, mutation ent.Mutation, name string) (int64, error) {
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

func mutationOldString(ctx context.Context, mutation ent.Mutation, name string) (string, error) {
	value, err := mutation.OldField(ctx, name)
	if err != nil {
		return "", fmt.Errorf("load existing %s: %w", name, err)
	}
	result, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("unexpected old type %T for %s", value, name)
	}
	return result, nil
}

func mutationOldBytes(ctx context.Context, mutation ent.Mutation, name string) ([]byte, error) {
	value, err := mutation.OldField(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("load existing %s: %w", name, err)
	}
	result, ok := value.([]byte)
	if !ok {
		return nil, fmt.Errorf("unexpected old type %T for %s", value, name)
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
