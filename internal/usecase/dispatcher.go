package usecase

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
)

type OutboxDispatcher struct {
	fileRepo     file.FileRepository
	jobPublisher file.EmbeddingJobPublisher
}

func NewOutboxDispatcher(fileRepo file.FileRepository, jobPublisher file.EmbeddingJobPublisher) *OutboxDispatcher {
	return &OutboxDispatcher{
		fileRepo:     fileRepo,
		jobPublisher: jobPublisher,
	}
}

func (d *OutboxDispatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	slog.Info("Outbox dispatcher worker started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Outbox dispatcher worker stopping (context cancelled)")
			return
		case <-ticker.C:
			d.dispatchPendingJobs(ctx)
		}
	}
}

func (d *OutboxDispatcher) dispatchPendingJobs(ctx context.Context) {
	if d.jobPublisher == nil || !d.jobPublisher.IsAvailable() {
		return
	}

	jobs, err := d.fileRepo.GetPendingOutboxJobs(ctx, 20)
	if err != nil {
		slog.Error("outbox dispatcher: failed to fetch pending jobs", "error", err)
		return
	}

	for _, oj := range jobs {
		var job contract.EmbeddingJob
		if err := json.Unmarshal([]byte(oj.Payload), &job); err != nil {
			slog.Error("outbox dispatcher: failed to unmarshal outbox job payload, deleting malformed job", "job_id", oj.ID, "error", err)
			_ = d.fileRepo.DeleteOutboxJob(ctx, oj.ID)
			continue
		}

		slog.Info("outbox dispatcher: dispatching job", "job_id", job.JobID, "file_id", job.FileID)
		if err := d.jobPublisher.PublishEmbeddingJob(ctx, job); err != nil {
			slog.Error("outbox dispatcher: failed to publish embedding job", "job_id", job.JobID, "error", err)
			continue
		}

		// Delete from outbox on success
		if err := d.fileRepo.DeleteOutboxJob(ctx, oj.ID); err != nil {
			slog.Error("outbox dispatcher: failed to delete outbox job from database", "job_id", oj.ID, "error", err)
		}
	}
}
