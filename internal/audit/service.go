package audit

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/auditevent"
)

type Service struct {
	db          *ent.Client
	lastCleanup atomic.Int64
}

type Entry struct {
	ID           string
	UserID       string
	Action       string
	ResourceKind string
	ResourceID   string
	Outcome      string
	RequestID    string
	Detail       string
}

func NewService(db *ent.Client) (*Service, error) {
	if db == nil {
		return nil, errors.New("audit service: nil database")
	}
	return &Service{db: db}, nil
}

func (s *Service) Record(ctx context.Context, entry Entry) error {
	if err := s.RecordWithClient(ctx, s.db, entry); err != nil {
		return err
	}
	s.cleanup(ctx)
	return nil
}

// RecordWithClient writes an idempotent audit entry using the supplied Ent
// client. Passing tx.Client() makes the audit row atomic with the caller's
// lifecycle metadata transaction.
func (s *Service) RecordWithClient(ctx context.Context, client *ent.Client, entry Entry) error {
	if client == nil {
		return errors.New("audit service: nil transaction client")
	}
	if entry.Outcome != "failure" {
		entry.Outcome = "success"
	}
	create := client.AuditEvent.Create().SetAction(entry.Action).SetResourceKind(entry.ResourceKind).SetResourceID(entry.ResourceID).SetOutcome(auditevent.Outcome(entry.Outcome)).SetRequestID(entry.RequestID).SetDetail(entry.Detail)
	if entry.ID != "" {
		create.SetID(entry.ID)
	}
	if entry.UserID != "" {
		create.SetUserID(entry.UserID)
	}
	if _, err := create.Save(ctx); err != nil {
		if entry.ID != "" && ent.IsConstraintError(err) {
			existing, lookupErr := client.AuditEvent.Get(ctx, entry.ID)
			if lookupErr != nil {
				return errors.Join(err, lookupErr)
			}
			if auditEntryMatches(existing, entry) {
				return nil
			}
			return fmt.Errorf("audit event ID %q conflicts with a different entry: %w", entry.ID, err)
		}
		return err
	}
	return nil
}

func auditEntryMatches(existing *ent.AuditEvent, entry Entry) bool {
	ownerMatches := entry.UserID == "" && existing.UserID == nil ||
		entry.UserID != "" && existing.UserID != nil && *existing.UserID == entry.UserID
	return ownerMatches && existing.Action == entry.Action &&
		existing.ResourceKind == entry.ResourceKind && existing.ResourceID == entry.ResourceID &&
		existing.Outcome == auditevent.Outcome(entry.Outcome) && existing.RequestID == entry.RequestID &&
		existing.Detail == entry.Detail
}

func (s *Service) cleanup(ctx context.Context) {
	now := time.Now()
	previous := s.lastCleanup.Load()
	if previous != 0 && now.Sub(time.Unix(previous, 0)) < 24*time.Hour {
		return
	}
	if !s.lastCleanup.CompareAndSwap(previous, now.Unix()) {
		return
	}
	_, _ = s.db.AuditEvent.Delete().Where(auditevent.CreatedAtLT(now.Add(-90 * 24 * time.Hour))).Exec(ctx)
}
