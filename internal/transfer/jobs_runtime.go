package transfer

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/transferjob"
)

func (s *Service) leavePending() {
	s.mu.Lock()
	s.pending--
	s.pendingCond.Broadcast()
	s.mu.Unlock()
}

func (s *Service) releaseJob(jobID string, preserveProgress bool) {
	s.mu.Lock()
	active := s.activeJobs[jobID]
	if active == nil {
		s.mu.Unlock()
		return
	}
	active.cancel(nil)
	active.stopExpiry()
	delete(s.activeJobs, jobID)
	if !preserveProgress {
		delete(s.progressPublished, jobID)
	}
	s.ownerJobs[active.ownerID]--
	if s.ownerJobs[active.ownerID] == 0 {
		delete(s.ownerJobs, active.ownerID)
	}
	s.scheduleQueuedResumesLocked(active.ownerID)
	s.mu.Unlock()
}

func (s *Service) enqueueResume(ownerID, jobID string) {
	s.mu.Lock()
	if !slices.ContainsFunc(s.resumeQueue[ownerID], func(queued *queuedResume) bool { return queued.jobID == jobID }) {
		s.resumeQueue[ownerID] = append(s.resumeQueue[ownerID], &queuedResume{jobID: jobID})
	}
	s.scheduleQueuedResumesLocked(ownerID)
	s.mu.Unlock()
	s.wakeResumeQueue()
}

func (s *Service) scheduleQueuedResumesLocked(ownerID string) {
	if s.closed || s.resumeScheduling[ownerID] || len(s.resumeQueue[ownerID]) == 0 || s.ownerJobs[ownerID] >= s.maxJobsPerOwner() {
		return
	}
	s.resumeScheduling[ownerID] = true
	s.wg.Go(func() { s.runQueuedResumes(ownerID) })
}

func (s *Service) runQueuedResumes(ownerID string) {
	for {
		s.mu.Lock()
		if s.closed || context.Cause(s.queueCtx) != nil || len(s.resumeQueue[ownerID]) == 0 || s.ownerJobs[ownerID] >= s.maxJobsPerOwner() {
			delete(s.resumeScheduling, ownerID)
			s.mu.Unlock()
			return
		}
		queued, wait := nextQueuedResume(s.resumeQueue[ownerID], s.resumeNow())
		s.mu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-s.queueWake:
				if !timer.Stop() {
					<-timer.C
				}
			case <-s.queueCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
			}
			continue
		}

		if _, err := s.startJob(s.queueCtx, ownerID, queued.jobID, true, transferjob.AttemptKindResume); err != nil {
			if errors.Is(err, ErrOwnerCapacity) {
				if s.finishResumeSchedulerForCapacity(ownerID) {
					continue
				}
				return
			}
			if errors.Is(err, ErrServiceClosed) || errors.Is(err, context.Canceled) {
				return
			}
			s.mu.Lock()
			if retryableResumeError(err) {
				queued.failures++
				queued.nextAttempt = s.resumeNow().Add(s.resumeJitter(resumeRetryDelay(queued.failures)))
				moveQueuedResumeToBack(s.resumeQueue[ownerID], queued)
			} else {
				removeQueuedResumeLocked(s, ownerID, queued)
				delete(s.progressPublished, queued.jobID)
			}
			s.mu.Unlock()
			s.logger.Error("Resume queued transfer failed", "owner_id", ownerID, "job_id", queued.jobID, "error", err)
			continue
		}
		s.mu.Lock()
		if queued.retryRequested {
			queued.retryRequested = false
			moveQueuedResumeToBack(s.resumeQueue[ownerID], queued)
		} else {
			removeQueuedResumeLocked(s, ownerID, queued)
		}
		s.mu.Unlock()
	}
}

func (s *Service) finishResumeSchedulerForCapacity(ownerID string) bool {
	if s.runnerHooks.beforeResumeCapacityClear != nil {
		s.runnerHooks.beforeResumeCapacityClear()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && context.Cause(s.queueCtx) == nil && len(s.resumeQueue[ownerID]) > 0 && s.ownerJobs[ownerID] < s.maxJobsPerOwner() {
		return true
	}
	delete(s.resumeScheduling, ownerID)
	return false
}

func (s *Service) requeueManagedResume(ownerID, jobID string) {
	s.mu.Lock()
	s.resumeFailures[jobID]++
	failures := s.resumeFailures[jobID]
	nextAttempt := s.resumeNow().Add(s.resumeJitter(resumeRetryDelay(failures)))
	queue := s.resumeQueue[ownerID]
	if index := slices.IndexFunc(queue, func(queued *queuedResume) bool { return queued.jobID == jobID }); index >= 0 {
		queue[index].retryRequested = true
		queue[index].failures = failures
		queue[index].nextAttempt = nextAttempt
	} else {
		s.resumeQueue[ownerID] = append(queue, &queuedResume{jobID: jobID, failures: failures, nextAttempt: nextAttempt})
	}
	s.scheduleQueuedResumesLocked(ownerID)
	s.mu.Unlock()
	s.wakeResumeQueue()
}

func nextQueuedResume(queue []*queuedResume, now time.Time) (*queuedResume, time.Duration) {
	var earliest *queuedResume
	for _, queued := range queue {
		if queued.nextAttempt.IsZero() || !queued.nextAttempt.After(now) {
			return queued, 0
		}
		if earliest == nil || queued.nextAttempt.Before(earliest.nextAttempt) {
			earliest = queued
		}
	}
	return earliest, earliest.nextAttempt.Sub(now)
}

func moveQueuedResumeToBack(queue []*queuedResume, target *queuedResume) {
	index := slices.Index(queue, target)
	if index < 0 || index == len(queue)-1 {
		return
	}
	copy(queue[index:], queue[index+1:])
	queue[len(queue)-1] = target
}

func removeQueuedResumeLocked(s *Service, ownerID string, target *queuedResume) {
	queue := s.resumeQueue[ownerID]
	index := slices.Index(queue, target)
	if index < 0 {
		return
	}
	queue = append(queue[:index], queue[index+1:]...)
	if len(queue) == 0 {
		delete(s.resumeQueue, ownerID)
		return
	}
	s.resumeQueue[ownerID] = queue
}

func retryableResumeError(err error) bool {
	return !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalidState)
}

func resumeRetryDelay(failures int) time.Duration {
	shift := min(max(failures-1, 0), 6)
	return min(time.Minute, time.Second*time.Duration(1<<shift))
}

func jitterResumeDelay(delay time.Duration) time.Duration {
	spread := delay / 4
	if spread <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int64N(int64(spread)+1))
}

func (s *Service) resetManagedResumeFailures(jobID string) {
	s.mu.Lock()
	delete(s.resumeFailures, jobID)
	s.mu.Unlock()
}

func (s *Service) wakeResumeQueue() {
	select {
	case s.queueWake <- struct{}{}:
	default:
	}
}

func jobView(row *ent.TransferJob) JobView {
	var startedAt, finishedAt *time.Time
	if row.StartedAt != nil {
		startedAt = new(row.StartedAt.UTC())
	}
	if row.FinishedAt != nil {
		finishedAt = new(row.FinishedAt.UTC())
	}
	return JobView{
		ID: row.ID, ClientID: row.ClientID, RemoteShareID: row.RemoteShareID, Status: string(row.Status),
		TotalBytes: row.TotalBytes, ReceivedBytes: row.ReceivedBytes, ExpiresAt: row.ExpiresAt.UTC(), ErrorCode: ErrorCode(row.ErrorCode),
		StartedAt: startedAt, FinishedAt: finishedAt, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

func itemView(row *ent.TransferItem) ItemView {
	var startedAt, finishedAt *time.Time
	if row.StartedAt != nil {
		startedAt = new(row.StartedAt.UTC())
	}
	if row.FinishedAt != nil {
		finishedAt = new(row.FinishedAt.UTC())
	}
	return ItemView{
		ID: row.ID, JobID: row.JobID, VirtualPath: row.VirtualPath, Size: row.SizeBytes,
		Status: string(row.Status), ReceivedBytes: row.ReceivedBytes, CompletedBlocks: len(row.CompletedBlocks), MTime: row.Mtime.UTC(),
		StartedAt: startedAt, FinishedAt: finishedAt, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}
