package health

import (
	"context"
	"encoding/json"
	"fmt"

	"lifeops/internal/store"
)

type Service struct {
	store *store.Store
}

func NewService(store *store.Store) *Service {
	return &Service{store: store}
}

func (s *Service) IngestWorkouts(ctx context.Context, batch WorkoutsBatch) error {
	workouts := make([]store.Workout, 0, len(batch.Items))
	for _, item := range batch.Items {
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal workout: %w", err)
		}
		workouts = append(workouts, store.Workout{
			ID:               item.ID,
			WorkoutType:      item.WorkoutType,
			Start:            item.Start,
			End:              item.End,
			DurationMinutes:  item.DurationMinutes,
			DistanceMeters:   item.DistanceMeters,
			Calories:         item.Calories,
			AverageHeartRate: item.AverageHeartRate,
			Raw:              raw,
		})
	}
	if err := s.store.UpsertWorkouts(ctx, workouts); err != nil {
		return err
	}
	return s.applyDeletions(ctx, batch.Deleted)
}

func (s *Service) IngestSleep(ctx context.Context, batch SleepBatch) error {
	sleeps := make([]store.Sleep, 0, len(batch.Items))
	for _, item := range batch.Items {
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal sleep: %w", err)
		}
		sleeps = append(sleeps, store.Sleep{
			ID:           item.ID,
			Start:        item.Start,
			End:          item.End,
			TotalMinutes: item.TotalMinutes,
			RemMinutes:   item.Breakdown.RemMinutes,
			DeepMinutes:  item.Breakdown.DeepMinutes,
			CoreMinutes:  item.Breakdown.CoreMinutes,
			AwakeMinutes: item.Breakdown.AwakeMinutes,
			Raw:          raw,
		})
	}
	if err := s.store.UpsertSleep(ctx, sleeps); err != nil {
		return err
	}
	return s.applyDeletions(ctx, batch.Deleted)
}

func (s *Service) IngestMetrics(ctx context.Context, batch MetricsBatch) error {
	metrics := make([]store.Metric, 0, len(batch.Items))
	for _, item := range batch.Items {
		raw, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal metric: %w", err)
		}
		metrics = append(metrics, store.Metric{
			ID:    item.ID,
			Kind:  item.Kind,
			Start: item.Start,
			End:   item.End,
			Value: item.Value,
			Unit:  item.Unit,
			Raw:   raw,
		})
	}
	if err := s.store.UpsertMetrics(ctx, metrics); err != nil {
		return err
	}
	return s.applyDeletions(ctx, batch.Deleted)
}

func (s *Service) applyDeletions(ctx context.Context, deletions []Deletion) error {
	byType := map[string][]string{}
	for _, d := range deletions {
		byType[d.SampleType] = append(byType[d.SampleType], d.ID)
	}
	for sampleType, ids := range byType {
		if err := s.store.SoftDelete(ctx, sampleType, ids); err != nil {
			return err
		}
	}
	return nil
}
