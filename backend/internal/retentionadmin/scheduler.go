package retentionadmin

import (
	"context"
	"regexp"
)

var workerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type Scheduler struct {
	repository Repository
	workerID   string
}

func NewScheduler(repository Repository, workerID string) (*Scheduler, error) {
	if repository == nil || !workerPattern.MatchString(workerID) {
		return nil, ErrInvalid
	}
	return &Scheduler{repository: repository, workerID: workerID}, nil
}

func (s *Scheduler) RunOnce(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	return s.repository.AdvanceDue(ctx, s.workerID, limit)
}
