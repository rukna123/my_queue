// Package streamer reads telemetry CSV data and streams it to the MQ service.
package streamer

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// TelemetryRow is a single row parsed from the telemetry CSV.
// Timestamp is set to time.Now().UTC() when the row is processed for
// streaming (in readLoop), not when it is originally read from disk.
type TelemetryRow struct {
	Timestamp  time.Time // set to time.Now().UTC() when the row is processed
	MetricName string
	GPUID      string
	UUID       string
	ModelName  string
	Container  string
	Pod        string
	Namespace  string
	Value      float64
	LabelsRaw  string
}

// ReadCSV loads and parses a telemetry CSV file.
// Expected header (first row is skipped):
//
//	timestamp, metric_name, gpu_id, uuid, model_name, container, pod, namespace, value, labels_raw
func ReadCSV(path string) ([]TelemetryRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open csv %q: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1 // allow variable columns for safety

	// Read and discard the header row.
	if _, err := r.Read(); err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}

	var rows []TelemetryRow
	lineNum := 1 // 1-based, header was line 1
	for {
		lineNum++
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv line %d: %w", lineNum, err)
		}
		if len(record) < 10 {
			return nil, fmt.Errorf("csv line %d: expected 10 fields, got %d", lineNum, len(record))
		}

		val, _ := strconv.ParseFloat(strings.TrimSpace(record[8]), 64)

		// Timestamp is intentionally left as zero value here;
		// it is stamped with time.Now().UTC() in readLoop when
		// the row is processed into a batch.
		_ = strings.TrimSpace(record[0]) // original CSV timestamp (unused)

		rows = append(rows, TelemetryRow{
			MetricName: strings.TrimSpace(record[1]),
			GPUID:      strings.TrimSpace(record[2]),
			UUID:       strings.TrimSpace(record[3]),
			ModelName:  strings.TrimSpace(record[4]),
			Container:  strings.TrimSpace(record[5]),
			Pod:        strings.TrimSpace(record[6]),
			Namespace:  strings.TrimSpace(record[7]),
			Value:      val,
			LabelsRaw:  strings.TrimSpace(record[9]),
		})
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("csv %q contains no data rows", path)
	}
	return rows, nil
}
