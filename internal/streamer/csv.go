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
// When a row is processed for streaming, its Timestamp is set to
// time.Now().UTC() and the entire CSV is rewritten to disk so the file
// always reflects the last-processed time for every row.
type TelemetryRow struct {
	Timestamp  time.Time
	MetricName string
	GPUID      string
	Device     string
	UUID       string
	ModelName  string
	Hostname   string
	Container  string
	Pod        string
	Namespace  string
	Value      float64
	LabelsRaw  string
}

// csvHeader is written as the first row of every CSV we produce.
// Column order: timestamp, metric_name, gpu_id, device, uuid,
// model_name, hostname, container, pod, namespace, value, labels_raw
var csvHeader = []string{
	"timestamp", "metric_name", "gpu_id", "device", "uuid",
	"model_name", "hostname", "container", "pod", "namespace",
	"value", "labels_raw",
}

// ReadCSV loads and parses a telemetry CSV file.
// Expected header (first row is skipped):
//
//	timestamp, metric_name, gpu_id, device, uuid, model_name, hostname, container, pod, namespace, value, labels_raw
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
		if len(record) < 12 {
			return nil, fmt.Errorf("csv line %d: expected 12 fields, got %d", lineNum, len(record))
		}

		val, _ := strconv.ParseFloat(strings.TrimSpace(record[10]), 64)

		// Parse the timestamp from CSV. If it cannot be parsed (e.g. first
		// run with placeholder data), it stays as the zero time.
		ts, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(record[0]))

		rows = append(rows, TelemetryRow{
			Timestamp:  ts,                            // [0]
			MetricName: strings.TrimSpace(record[1]),  // [1]
			GPUID:      strings.TrimSpace(record[2]),  // [2]
			Device:     strings.TrimSpace(record[3]),  // [3]
			UUID:       strings.TrimSpace(record[4]),  // [4]
			ModelName:  strings.TrimSpace(record[5]),  // [5]
			Hostname:   strings.TrimSpace(record[6]),  // [6]
			Container:  strings.TrimSpace(record[7]),  // [7]
			Pod:        strings.TrimSpace(record[8]),  // [8]
			Namespace:  strings.TrimSpace(record[9]),  // [9]
			Value:      val,                           // [10]
			LabelsRaw:  strings.TrimSpace(record[11]), // [11]
		})
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("csv %q contains no data rows", path)
	}
	return rows, nil
}

// WriteCSV atomically rewrites the CSV file with the current state of rows.
// It writes to a temporary file first, then renames it over the original to
// avoid partial writes on crash.
func WriteCSV(path string, rows []TelemetryRow) error {
	tmp := path + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp csv: %w", err)
	}

	w := csv.NewWriter(f)

	if err := w.Write(csvHeader); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write header: %w", err)
	}

	for _, row := range rows {
		ts := ""
		if !row.Timestamp.IsZero() {
			ts = row.Timestamp.Format(time.RFC3339Nano)
		}

		record := []string{
			ts,
			row.MetricName,
			row.GPUID,
			row.Device,
			row.UUID,
			row.ModelName,
			row.Hostname,
			row.Container,
			row.Pod,
			row.Namespace,
			strconv.FormatFloat(row.Value, 'f', -1, 64),
			row.LabelsRaw,
		}
		if err := w.Write(record); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("write row: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("csv flush: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp csv: %w", err)
	}

	return os.Rename(tmp, path)
}
