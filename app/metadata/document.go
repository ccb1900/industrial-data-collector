package metadata

import "gocordis-csv-collector/app/model"

// MergeDocument returns the Business Metadata for one CSV file after both
// Path Metadata and the CSV Document Metadata Section have been read.
//
// For ordinary CSV (no configured metadata section) the existing path
// metadata keys are returned unchanged, preserving the historical flat
// key contract. Once a CSV document declares a structured Metadata Section,
// path values are additionally exposed under path.* and CSV values under
// csv.* so equal business keys from different contexts never overwrite each
// other (CM-10/CM-11).
func MergeDocument(path model.Metadata, doc model.CSVDocument) model.Metadata {
	out := path.Clone()
	if !doc.Structured {
		return out
	}
	for k, v := range path.Values {
		out.Values["path."+k] = v
	}
	for k, v := range doc.Metadata.Values {
		out.Values["csv."+k] = v
	}
	return out
}
