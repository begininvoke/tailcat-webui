package transfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/sharefile"
	"github.com/ca-x/tailcat-webui/ent/transferitem"
)

var errConfiguredIneligible = errors.New("transfer exceeds current configured limits")
var errConfiguredExpired = errors.New("transfer exceeds current configured lifetime")

func (s *Service) effectiveExpiry(createdAt, storedExpiry time.Time) time.Time {
	configuredExpiry := createdAt.UTC().Add(s.limits.Expiry)
	if storedExpiry.Before(configuredExpiry) {
		return storedExpiry.UTC()
	}
	return configuredExpiry
}

func (s *Service) validateShareLimits(ctx context.Context, row *ent.TransferShare, now time.Time) error {
	if row == nil || !s.effectiveExpiry(row.CreatedAt, row.ExpiresAt).After(now.UTC()) {
		return errors.Join(errConfiguredIneligible, errConfiguredExpired)
	}
	files, err := s.db.ShareFile.Query().Where(sharefile.UserIDEQ(row.UserID), sharefile.ShareIDEQ(row.ID)).All(ctx)
	if err != nil {
		return fmt.Errorf("load share files for configured limits: %w", err)
	}
	if len(files) > s.limits.MaxFilesPerShare || row.FileCount != len(files) {
		return fmt.Errorf("%w: share file count", errConfiguredIneligible)
	}
	var total int64
	for _, file := range files {
		if file.SizeBytes < 0 || file.SizeBytes > s.limits.MaxFileBytes || total > s.limits.MaxShareBytes-file.SizeBytes || validateVirtualPath(file.VirtualPath) != nil {
			return fmt.Errorf("%w: share file metadata", errConfiguredIneligible)
		}
		total += file.SizeBytes
	}
	if total != row.TotalBytes || total > s.limits.MaxShareBytes {
		return fmt.Errorf("%w: share byte total", errConfiguredIneligible)
	}
	usage, err := s.storage.Usage(ctx, row.UserID, row.ID)
	if err != nil {
		return fmt.Errorf("load owner usage for configured share limits: %w", err)
	}
	if usage.OwnerBytes > s.storage.limits.MaxOwnerBytes {
		return fmt.Errorf("%w: owner byte total", errConfiguredIneligible)
	}
	return nil
}

func (s *Service) validateJobLimits(ctx context.Context, row *ent.TransferJob, now time.Time) error {
	if row == nil || !s.effectiveExpiry(row.CreatedAt, row.ExpiresAt).After(now.UTC()) {
		return errors.Join(errConfiguredIneligible, errConfiguredExpired)
	}
	items, err := s.db.TransferItem.Query().Where(transferitem.UserIDEQ(row.UserID), transferitem.JobIDEQ(row.ID)).All(ctx)
	if err != nil {
		return fmt.Errorf("load job items for configured limits: %w", err)
	}
	if len(items) > s.limits.MaxFilesPerShare {
		return fmt.Errorf("%w: job item count", errConfiguredIneligible)
	}
	var total int64
	for _, item := range items {
		if item.SizeBytes < 0 || item.SizeBytes > s.limits.MaxFileBytes || total > s.limits.MaxJobBytes-item.SizeBytes || validateVirtualPath(item.VirtualPath) != nil {
			return fmt.Errorf("%w: job item metadata", errConfiguredIneligible)
		}
		total += item.SizeBytes
	}
	if total != row.TotalBytes || total > s.limits.MaxJobBytes {
		return fmt.Errorf("%w: job byte total", errConfiguredIneligible)
	}
	usage, err := s.storage.Usage(ctx, row.UserID, row.ID)
	if err != nil {
		return fmt.Errorf("load owner usage for configured job limits: %w", err)
	}
	if usage.OwnerBytes > s.storage.limits.MaxOwnerBytes {
		return fmt.Errorf("%w: owner byte total", errConfiguredIneligible)
	}
	return nil
}
