package parser

import (
	"fmt"
	"strconv"

	"gocordis-csv-collector/app/errs"
)

// DocumentConfig describes the explicit CSV Metadata Section / Data Section
// boundary used by the structured CSV parser.
//
// Rows are physical rows counted from 1. key_value means rows StartRow..EndRow
// are `key,value` metadata rows; HeaderRow is the Data Section header row.
// Blank rows between EndRow and HeaderRow are separators and never parsed.
type DocumentConfig struct {
	Mode      string
	StartRow  int
	EndRow    int
	HeaderRow int
}

// Enabled reports whether a structured Metadata Section must be parsed.
func (c DocumentConfig) Enabled() bool { return c.Mode == ModeKeyValue }

const (
	// ModeNone keeps the existing CSV row behavior (Metadata is empty).
	ModeNone = "none"
	// ModeKeyValue parses an explicit key,value Metadata Section.
	ModeKeyValue = "key_value"
)

// ParseDocumentConfig decodes the `csv` table from a csv-parser component
// config:
//
//	[components.config.csv.metadata]
//	mode = "key_value"
//	start_row = 1
//	end_row = 5
//
//	[components.config.csv.data]
//	header_row = 7
func ParseDocumentConfig(componentConfig map[string]any) (DocumentConfig, error) {
	cfg := DocumentConfig{Mode: ModeNone}
	if componentConfig == nil {
		return cfg, nil
	}
	rawCSV, ok := componentConfig["csv"]
	if !ok || rawCSV == nil {
		return cfg, nil
	}
	csvMap, ok := rawCSV.(map[string]any)
	if !ok {
		return cfg, fmt.Errorf("%w: csv must be a table", errs.ErrInvalidConfig)
	}
	metaRaw, hasMeta := csvMap["metadata"]
	dataRaw, hasData := csvMap["data"]
	if !hasMeta && !hasData {
		return cfg, fmt.Errorf("%w: csv table must contain metadata and/or data", errs.ErrInvalidConfig)
	}
	metaMap := map[string]any{}
	if hasMeta {
		var ok bool
		metaMap, ok = metaRaw.(map[string]any)
		if !ok {
			return cfg, fmt.Errorf("%w: csv.metadata must be a table", errs.ErrInvalidConfig)
		}
	}
	dataMap := map[string]any{}
	if hasData {
		var ok bool
		dataMap, ok = dataRaw.(map[string]any)
		if !ok {
			return cfg, fmt.Errorf("%w: csv.data must be a table", errs.ErrInvalidConfig)
		}
	}
	modeRaw, ok := metaMap["mode"]
	if !ok || modeRaw == nil {
		if !hasMeta {
			return cfg, fmt.Errorf("%w: csv.metadata.mode is required when csv.data is configured", errs.ErrInvalidConfig)
		}
		return cfg, fmt.Errorf("%w: csv.metadata.mode is required", errs.ErrInvalidConfig)
	}
	mode, ok := modeRaw.(string)
	if !ok {
		return cfg, fmt.Errorf("%w: csv.metadata.mode must be a string", errs.ErrInvalidConfig)
	}
	cfg.Mode = mode
	switch mode {
	case ModeNone:
		if len(metaMap) > 1 || len(dataMap) > 0 {
			return cfg, fmt.Errorf("%w: mode=%s accepts no row configuration", errs.ErrInvalidConfig, ModeNone)
		}
		return cfg, nil
	case ModeKeyValue:
		start, err := docInt(metaMap, "start_row")
		if err != nil {
			return cfg, fmt.Errorf("%w: csv.metadata.start_row: %v", errs.ErrInvalidConfig, err)
		}
		end, err := docInt(metaMap, "end_row")
		if err != nil {
			return cfg, fmt.Errorf("%w: csv.metadata.end_row: %v", errs.ErrInvalidConfig, err)
		}
		header, err := docInt(dataMap, "header_row")
		if err != nil {
			return cfg, fmt.Errorf("%w: csv.data.header_row: %v", errs.ErrInvalidConfig, err)
		}
		if start < 1 {
			return cfg, fmt.Errorf("%w: csv.metadata.start_row must be >= 1", errs.ErrInvalidConfig)
		}
		if end < start {
			return cfg, fmt.Errorf("%w: csv.metadata.end_row must be >= start_row", errs.ErrInvalidConfig)
		}
		if header <= end {
			return cfg, fmt.Errorf("%w: csv.data.header_row must be > metadata.end_row", errs.ErrInvalidConfig)
		}
		cfg.StartRow, cfg.EndRow, cfg.HeaderRow = start, end, header
		return cfg, nil
	default:
		return cfg, fmt.Errorf("%w: unknown csv.metadata.mode %q", errs.ErrInvalidConfig, mode)
	}
}

func docInt(m map[string]any, key string) (int, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return 0, fmt.Errorf("missing integer field")
	}
	n, err := strconv.Atoi(fmt.Sprint(raw))
	if err != nil {
		return 0, fmt.Errorf("must be an integer")
	}
	return n, nil
}
