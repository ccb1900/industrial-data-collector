package metadata

import "gocordis-csv-collector/app/model"

// MergeDocument returns the Business Metadata for one CSV file after both
// Path Metadata and the CSV Document Metadata Section have been read.
//
// For ordinary CSV (no configured metadata section) the existing path
// metadata keys are returned unchanged, preserving the historical flat
// key contract. Once a CSV document declares a structured Metadata Section,
// path values are exposed under path.* and CSV values under csv.*; raw path
// keys are intentionally not leaked back into the flat key space so every
// semantic layer has an explicit namespace (CM-10/CM-10A/CM-11).
func MergeDocument(path model.Metadata, doc model.CSVDocument) model.Metadata {
	if !doc.Structured {
		return path.Clone()
	}
	out := model.NewMetadata()
	for k, v := range path.Values {
		out.Values["path."+k] = v
	}
	for k, v := range doc.Metadata.Values {
		out.Values["csv."+k] = v
	}
	return out
}
