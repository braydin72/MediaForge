package store

import (
	"database/sql"
	"time"
)

// Outcome values for ProcessingRecord.Outcome.
const (
	OutcomeSuccess     = "success"
	OutcomeReviewQueue = "review_queue"
	OutcomeDiscarded   = "discarded"
)

// ProcessingRecord is a per-file intake pipeline record. Fields that are not
// yet known (e.g. encode results for a file still in the pipeline) are left at
// their zero values and filled in later via UpdateProcessingRecord.
type ProcessingRecord struct {
	ID                 string
	Filename           string
	SourcePath         string
	DetectedCodec      string
	Container          string
	Resolution         string // "WIDTHxHEIGHT", e.g. "1920x1080"
	DurationSecs       int64
	MatchedTitle       string
	MatchedYear        int
	TMDBId             string
	TVDBId             string
	PipelineMode       string // "full" | "encode_only" | "encode_only_custom"
	OriginalSizeBytes  int64
	OutputSizeBytes    int64   // 0 = not encoded / pending
	SizeReductionPct   float64 // 0 = not reduced
	EncodeDurationSecs int64   // 0 = not encoded
	EncodePreset       string
	EncodeSpeed        string
	EncodeGPU          string
	EncodeFPS          float64 // 0 = not encoded
	Outcome            string  // OutcomeSuccess | OutcomeReviewQueue | OutcomeDiscarded
	FailureReason      string  // non-empty when Outcome != OutcomeSuccess
	CreatedAt          time.Time
}

// IntakeStats holds aggregate intake pipeline statistics computed from
// processing_records. Lifetime and period use the same fields; the difference
// is the cutoff timestamp applied when querying.
type IntakeStats struct {
	// Lifetime totals — since last lifetime reset, or all-time if never reset.
	LifetimeFilesProcessed    int
	LifetimeStorageSavedBytes int64
	LifetimeAvgReductionPct   float64
	LifetimeEncodeSuccessRate float64 // 0–100
	LifetimeAvgEncodeFPS      float64
	LifetimeResetAt           time.Time // zero = never reset

	// Period totals — since last period reset, or all-time if never reset.
	PeriodFilesProcessed     int
	PeriodStorageSavedBytes  int64
	PeriodAvgReductionPct    float64
	PeriodEncodeSuccessRate  float64
	PeriodAvgEncodeFPS       float64
	PeriodResetAt            time.Time // zero = never reset; shown on dashboard

	// Rolling 30-day failure analysis.
	MostCommonFailureReason string
	MostCommonFailureCount  int
}

// AddProcessingRecord inserts a new per-file processing record.
// The caller is responsible for setting a unique ID (e.g. uuid.New().String()).
func (s *SQLiteStore) AddProcessingRecord(r *ProcessingRecord) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO processing_records (
			id, filename, source_path,
			detected_codec, container, resolution, duration_secs,
			matched_title, matched_year, tmdb_id, tvdb_id, pipeline_mode,
			original_size_bytes, output_size_bytes, size_reduction_pct,
			encode_duration_secs, encode_preset, encode_speed, encode_gpu, encode_fps,
			outcome, failure_reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Filename, r.SourcePath,
		r.DetectedCodec, r.Container, r.Resolution, r.DurationSecs,
		r.MatchedTitle, r.MatchedYear, r.TMDBId, r.TVDBId, r.PipelineMode,
		r.OriginalSizeBytes, r.OutputSizeBytes, r.SizeReductionPct,
		r.EncodeDurationSecs, r.EncodePreset, r.EncodeSpeed, r.EncodeGPU, r.EncodeFPS,
		r.Outcome, r.FailureReason, formatTime(r.CreatedAt),
	)
	return err
}

// UpdateProcessingRecord fills in encode-phase results on an existing record
// once transcoding completes.
func (s *SQLiteStore) UpdateProcessingRecord(id string, outputSizeBytes int64, sizeReductionPct float64, encodeDurationSecs int64, encodeFPS float64, outcome, failureReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE processing_records
		SET output_size_bytes = ?, size_reduction_pct = ?,
		    encode_duration_secs = ?, encode_fps = ?,
		    outcome = ?, failure_reason = ?
		WHERE id = ?`,
		outputSizeBytes, sizeReductionPct,
		encodeDurationSecs, encodeFPS,
		outcome, failureReason, id,
	)
	return err
}

// GetProcessingRecords returns a page of processing records, newest first.
func (s *SQLiteStore) GetProcessingRecords(limit, offset int) ([]ProcessingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, filename, source_path,
		       detected_codec, container, resolution, duration_secs,
		       matched_title, matched_year, tmdb_id, tvdb_id, pipeline_mode,
		       original_size_bytes, output_size_bytes, size_reduction_pct,
		       encode_duration_secs, encode_preset, encode_speed, encode_gpu, encode_fps,
		       outcome, failure_reason, created_at
		FROM processing_records
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ProcessingRecord
	for rows.Next() {
		var r ProcessingRecord
		var createdAt string
		if err := rows.Scan(
			&r.ID, &r.Filename, &r.SourcePath,
			&r.DetectedCodec, &r.Container, &r.Resolution, &r.DurationSecs,
			&r.MatchedTitle, &r.MatchedYear, &r.TMDBId, &r.TVDBId, &r.PipelineMode,
			&r.OriginalSizeBytes, &r.OutputSizeBytes, &r.SizeReductionPct,
			&r.EncodeDurationSecs, &r.EncodePreset, &r.EncodeSpeed, &r.EncodeGPU, &r.EncodeFPS,
			&r.Outcome, &r.FailureReason, &createdAt,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTime(createdAt)
		records = append(records, r)
	}
	return records, rows.Err()
}

// GetIntakeStats computes aggregate stats from processing_records using the
// stored reset timestamps as window cutoffs.
func (s *SQLiteStore) GetIntakeStats() (IntakeStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lifetimeResetAt := s.getMetaTimeUnlocked("intake_lifetime_reset_at")
	periodResetAt := s.getMetaTimeUnlocked("intake_period_reset_at")

	var stats IntakeStats
	stats.LifetimeResetAt = lifetimeResetAt
	stats.PeriodResetAt = periodResetAt

	var err error
	stats.LifetimeFilesProcessed, stats.LifetimeStorageSavedBytes,
		stats.LifetimeAvgReductionPct, stats.LifetimeEncodeSuccessRate,
		stats.LifetimeAvgEncodeFPS, err = s.aggregateStatsUnlocked(lifetimeResetAt)
	if err != nil {
		return stats, err
	}

	stats.PeriodFilesProcessed, stats.PeriodStorageSavedBytes,
		stats.PeriodAvgReductionPct, stats.PeriodEncodeSuccessRate,
		stats.PeriodAvgEncodeFPS, err = s.aggregateStatsUnlocked(periodResetAt)
	if err != nil {
		return stats, err
	}

	stats.MostCommonFailureReason, stats.MostCommonFailureCount, err = s.mostCommonFailureUnlocked()
	return stats, err
}

// aggregateStatsUnlocked computes aggregate stats for records created at or
// after since. A zero since includes all records. Caller must hold s.mu.RLock.
func (s *SQLiteStore) aggregateStatsUnlocked(since time.Time) (files int, savedBytes int64, avgReduction, successRate, avgFPS float64, err error) {
	const baseQ = `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN output_size_bytes > 0 THEN original_size_bytes - output_size_bytes ELSE 0 END), 0),
			COALESCE(AVG(NULLIF(size_reduction_pct, 0)), 0.0),
			CASE WHEN COUNT(*) > 0
				THEN 100.0 * SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) / COUNT(*)
				ELSE 0.0 END,
			COALESCE(AVG(NULLIF(encode_fps, 0)), 0.0)
		FROM processing_records`

	var row *sql.Row
	if since.IsZero() {
		row = s.db.QueryRow(baseQ)
	} else {
		row = s.db.QueryRow(baseQ+" WHERE created_at >= ?", formatTime(since))
	}
	err = row.Scan(&files, &savedBytes, &avgReduction, &successRate, &avgFPS)
	return
}

// mostCommonFailureUnlocked returns the most frequent failure_reason over the
// last 30 days. Caller must hold s.mu.RLock.
func (s *SQLiteStore) mostCommonFailureUnlocked() (reason string, count int, err error) {
	cutoff := formatTime(time.Now().UTC().AddDate(0, 0, -30))
	row := s.db.QueryRow(`
		SELECT failure_reason, COUNT(*) AS cnt
		FROM processing_records
		WHERE outcome != 'success'
		  AND failure_reason != ''
		  AND created_at >= ?
		GROUP BY failure_reason
		ORDER BY cnt DESC
		LIMIT 1`, cutoff)
	err = row.Scan(&reason, &count)
	if err == sql.ErrNoRows {
		return "", 0, nil
	}
	return reason, count, err
}

// getMetaTimeUnlocked reads a time value from stats_metadata.
// Returns zero time if the key is missing or its value is empty.
// Caller must hold s.mu.RLock.
func (s *SQLiteStore) getMetaTimeUnlocked(key string) time.Time {
	var val string
	_ = s.db.QueryRow(`SELECT value FROM stats_metadata WHERE key = ?`, key).Scan(&val)
	if val == "" {
		return time.Time{}
	}
	return parseTime(val)
}

// ResetIntakeLifetime records now as the lifetime reset point. Subsequent
// GetIntakeStats calls will only count records created after this timestamp.
func (s *SQLiteStore) ResetIntakeLifetime() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		UPDATE stats_metadata
		SET value = ?, updated_at = datetime('now')
		WHERE key = 'intake_lifetime_reset_at'`,
		formatTime(time.Now().UTC()),
	)
	return err
}

// ResetIntakePeriod records now as the period reset point. The period reset
// date is shown on the dashboard so the user knows what window the stats cover.
func (s *SQLiteStore) ResetIntakePeriod() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		UPDATE stats_metadata
		SET value = ?, updated_at = datetime('now')
		WHERE key = 'intake_period_reset_at'`,
		formatTime(time.Now().UTC()),
	)
	return err
}
