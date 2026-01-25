package health

import "time"

type Deletion struct {
	ID         string `json:"id"`
	SampleType string `json:"sampleType"`
}

type Workout struct {
	ID               string    `json:"id"`
	WorkoutType      string    `json:"workoutType"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	DurationMinutes  float64   `json:"durationMinutes"`
	DistanceMeters   *float64  `json:"distanceMeters"`
	Calories         *float64  `json:"calories"`
	AverageHeartRate *float64  `json:"averageHeartRate"`
}

type SleepBreakdown struct {
	RemMinutes   float64 `json:"remMinutes"`
	DeepMinutes  float64 `json:"deepMinutes"`
	CoreMinutes  float64 `json:"coreMinutes"`
	AwakeMinutes float64 `json:"awakeMinutes"`
}

type Sleep struct {
	ID           string         `json:"id"`
	Start        time.Time      `json:"start"`
	End          time.Time      `json:"end"`
	TotalMinutes float64        `json:"totalMinutes"`
	Breakdown    SleepBreakdown `json:"breakdown"`
}

type Metric struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Value float64   `json:"value"`
	Unit  string    `json:"unit"`
}

type WorkoutsBatch struct {
	Items   []Workout  `json:"items"`
	Deleted []Deletion `json:"deleted"`
}

type SleepBatch struct {
	Items   []Sleep    `json:"items"`
	Deleted []Deletion `json:"deleted"`
}

type MetricsBatch struct {
	Items   []Metric   `json:"items"`
	Deleted []Deletion `json:"deleted"`
}
