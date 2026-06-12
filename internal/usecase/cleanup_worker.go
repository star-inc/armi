package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/star-inc/armi/pkgs/file"
)

type CleanupWorker struct {
	fileRepo file.FileRepository
	vectorDB file.VectorDB
	storage  file.Storage
}

func NewCleanupWorker(fileRepo file.FileRepository, vectorDB file.VectorDB, storage file.Storage) *CleanupWorker {
	return &CleanupWorker{
		fileRepo: fileRepo,
		vectorDB: vectorDB,
		storage:  storage,
	}
}

func (w *CleanupWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	slog.Info("Cleanup saga worker started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Cleanup saga worker stopping (context cancelled)")
			return
		case <-ticker.C:
			w.processPendingCleanups(ctx)
		}
	}
}

func (w *CleanupWorker) processPendingCleanups(ctx context.Context) {
	jobs, err := w.fileRepo.GetPendingCleanupJobs(ctx, 20)
	if err != nil {
		slog.Error("cleanup worker: failed to fetch pending cleanup jobs", "error", err)
		return
	}

	for _, job := range jobs {
		if job.RetryCount >= 10 {
			slog.Error("cleanup worker: job exceeded max retries, marking as dead", "job_id", job.ID, "file_id", job.FileID)
			job.Status = "dead"
			_ = w.fileRepo.UpdateCleanupJob(ctx, job)
			continue
		}

		slog.Info("cleanup worker: processing cleanup job", "job_id", job.ID, "file_id", job.FileID, "hash", job.Hash)
		
		// 1. Delete from vector DB
		if err := w.vectorDB.Delete(ctx, job.FileID); err != nil {
			slog.Error("cleanup worker: failed to delete vector", "file_id", job.FileID, "error", err)
			w.failJob(ctx, job)
			continue
		}

		// 2. Re-evaluate reference count before physical delete
		count, err := w.fileRepo.CountByHash(ctx, job.Hash)
		if err != nil {
			slog.Error("cleanup worker: failed to count hash references", "hash", job.Hash, "error", err)
			w.failJob(ctx, job)
			continue
		}

		// 3. Delete physical storage if no other files reference it and DeletePhysical is true
		if count == 0 && job.DeletePhysical {
			slog.Info("cleanup worker: deleting physical file", "key", job.StorageKey)
			if err := w.storage.Delete(ctx, job.StorageKey); err != nil {
				slog.Error("cleanup worker: failed to delete physical storage", "key", job.StorageKey, "error", err)
				w.failJob(ctx, job)
				continue
			}
		}

		// 4. Cleanup succeeded -> delete the cleanup job record
		if err := w.fileRepo.DeleteCleanupJob(ctx, job.ID); err != nil {
			slog.Error("cleanup worker: failed to delete cleanup job record", "job_id", job.ID, "error", err)
		} else {
			slog.Info("cleanup worker: successfully cleaned up resources", "job_id", job.ID, "file_id", job.FileID)
		}
	}
}

func (w *CleanupWorker) failJob(ctx context.Context, job *file.CleanupJob) {
	job.RetryCount++
	job.Status = "failed"
	if err := w.fileRepo.UpdateCleanupJob(ctx, job); err != nil {
		slog.Error("cleanup worker: failed to update cleanup job status", "job_id", job.ID, "error", err)
	}
}
