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

type deletionCause uint8

const (
	deletionRequested deletionCause = iota
	deletionExpired
	deletionLimit
)

type configuredIneligibleError struct {
	cause  deletionCause
	detail string
}

func (err *configuredIneligibleError) Error() string {
	return "transfer is ineligible under current configuration: " + err.detail
}

func newConfiguredIneligible(cause deletionCause, detail string) error {
	return &configuredIneligibleError{cause: cause, detail: detail}
}

func configuredDeletionCause(err error) (deletionCause, bool) {
	configured, ok := errors.AsType[*configuredIneligibleError](err)
	if !ok {
		return deletionRequested, false
	}
	return configured.cause, true
}

func (s *Service) effectiveExpiry(createdAt, storedExpiry time.Time) time.Time {
	configuredExpiry := createdAt.UTC().Add(s.limits.Expiry)
	if storedExpiry.Before(configuredExpiry) {
		return storedExpiry.UTC()
	}
	return configuredExpiry
}

func (s *Service) validateShareLimits(ctx context.Context, row *ent.TransferShare, now time.Time) error {
	if row == nil || !s.effectiveExpiry(row.CreatedAt, row.ExpiresAt).After(now.UTC()) {
		return newConfiguredIneligible(deletionExpired, "share lifetime")
	}
	files, err := s.db.ShareFile.Query().Where(sharefile.UserIDEQ(row.UserID), sharefile.ShareIDEQ(row.ID)).All(ctx)
	if err != nil {
		return fmt.Errorf("load share files for configured limits: %w", err)
	}
	if len(files) > s.limits.MaxFilesPerShare || row.FileCount != len(files) {
		return newConfiguredIneligible(deletionLimit, "share file count")
	}
	var total int64
	for _, file := range files {
		if file.SizeBytes < 0 || file.SizeBytes > s.limits.MaxFileBytes || total > s.limits.MaxShareBytes-file.SizeBytes || validateVirtualPath(file.VirtualPath) != nil {
			return newConfiguredIneligible(deletionLimit, "share file metadata")
		}
		total += file.SizeBytes
	}
	if total != row.TotalBytes || total > s.limits.MaxShareBytes {
		return newConfiguredIneligible(deletionLimit, "share byte total")
	}
	usage, err := s.storage.Usage(ctx, row.UserID, row.ID)
	if err != nil {
		return fmt.Errorf("load owner usage for configured share limits: %w", err)
	}
	if usage.OwnerBytes > s.storage.limits.MaxOwnerBytes {
		return newConfiguredIneligible(deletionLimit, "owner byte total")
	}
	return nil
}

func (s *Service) validateJobLimits(ctx context.Context, row *ent.TransferJob, now time.Time) error {
	if row == nil || !s.effectiveExpiry(row.CreatedAt, row.ExpiresAt).After(now.UTC()) {
		return newConfiguredIneligible(deletionExpired, "job lifetime")
	}
	items, err := s.db.TransferItem.Query().Where(transferitem.UserIDEQ(row.UserID), transferitem.JobIDEQ(row.ID)).All(ctx)
	if err != nil {
		return fmt.Errorf("load job items for configured limits: %w", err)
	}
	if len(items) > s.limits.MaxFilesPerShare {
		return newConfiguredIneligible(deletionLimit, "job item count")
	}
	var total int64
	for _, item := range items {
		if item.SizeBytes < 0 || item.SizeBytes > s.limits.MaxFileBytes || total > s.limits.MaxJobBytes-item.SizeBytes || validateVirtualPath(item.VirtualPath) != nil {
			return newConfiguredIneligible(deletionLimit, "job item metadata")
		}
		total += item.SizeBytes
	}
	if total != row.TotalBytes || total > s.limits.MaxJobBytes {
		return newConfiguredIneligible(deletionLimit, "job byte total")
	}
	usage, err := s.storage.Usage(ctx, row.UserID, row.ID)
	if err != nil {
		return fmt.Errorf("load owner usage for configured job limits: %w", err)
	}
	if usage.OwnerBytes > s.storage.limits.MaxOwnerBytes {
		return newConfiguredIneligible(deletionLimit, "owner byte total")
	}
	return nil
}
