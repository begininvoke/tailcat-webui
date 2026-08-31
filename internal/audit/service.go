package audit

import (
	"context"
	"errors"
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
	if entry.Outcome != "failure" {
		entry.Outcome = "success"
	}
	create := s.db.AuditEvent.Create().SetAction(entry.Action).SetResourceKind(entry.ResourceKind).SetResourceID(entry.ResourceID).SetOutcome(auditevent.Outcome(entry.Outcome)).SetRequestID(entry.RequestID).SetDetail(entry.Detail)
	if entry.UserID != "" {
		create.SetUserID(entry.UserID)
	}
	if _, err := create.Save(ctx); err != nil {
		return err
	}
	s.cleanup(ctx)
	return nil
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
