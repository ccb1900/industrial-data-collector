// Wire DTOs of the collector console. They are the camelCase shapes the
// frontend consumes; the bridge maps Application views onto them so the
// platform hub stays domain-free and the client contract stays stable.
package consolebridge

import (
	"time"

	"gocordis-csv-collector/app/model"
	"gocordis-csv-collector/app/query"
)

type uiSource struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Profiles []string `json:"profiles"`
	Status   string   `json:"status"`
}

func toUISource(v query.SourceView) uiSource {
	profiles := v.Profiles
	if profiles == nil {
		profiles = []string{}
	}
	return uiSource{ID: v.ID, Name: v.ID, Path: v.Path, Profiles: profiles, Status: v.Status}
}

type uiCollection struct {
	SourceID       string `json:"sourceId"`
	Date           string `json:"date"`
	Status         string `json:"status"`
	Note           string `json:"note,omitempty"`
	FilesTotal     int    `json:"filesTotal"`
	FilesCompleted int    `json:"filesCompleted"`
	FilesFailed    int    `json:"filesFailed"`
	Records        int64  `json:"records"`
	ChecksMismatch int    `json:"checksMismatch"`
}

func toUICollections(views []query.CollectionView) []uiCollection {
	out := make([]uiCollection, 0, len(views))
	for _, v := range views {
		out = append(out, uiCollection{
			SourceID:       v.SourceID,
			Date:           v.Date,
			Status:         v.Status,
			Note:           v.Note,
			FilesTotal:     v.FilesTotal,
			FilesCompleted: v.FilesCompleted,
			FilesFailed:    v.FilesFailed,
			Records:        v.Records,
			ChecksMismatch: v.ChecksMismatch,
		})
	}
	return out
}

// uiCheck flattens one file's consistency verdict for the `verifications`
// query: the three row counts side by side is the whole point of the panel —
// file, ledger and database must agree.
type uiCheck struct {
	SourceID   string   `json:"sourceId"`
	Date       string   `json:"date"`
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Status     string   `json:"status"`
	FileRows   int64    `json:"fileRows"`
	ParsedRows int64    `json:"parsedRows"`
	DBRows     int64    `json:"dbRows"`
	FileCols   int      `json:"fileCols"`
	StoredCols int      `json:"storedCols"`
	RaggedRows int64    `json:"raggedRows"`
	Unmapped   []string `json:"unmapped"`
	Note       string   `json:"note"`
	CheckedAt  string   `json:"checkedAt"`
}

func toUIChecks(views []model.FileCheck) []uiCheck {
	out := make([]uiCheck, 0, len(views))
	for _, c := range views {
		unmapped := c.Unmapped
		if unmapped == nil {
			unmapped = []string{}
		}
		out = append(out, uiCheck{
			SourceID:   string(c.Key.SourceID),
			Date:       c.Key.Date.String(),
			Name:       c.File.Name,
			Path:       c.File.Path,
			Status:     string(c.Status),
			FileRows:   c.FileRows,
			ParsedRows: c.ParsedRows,
			DBRows:     c.DBRows,
			FileCols:   c.FileCols,
			StoredCols: c.StoredCols,
			RaggedRows: c.RaggedRows,
			Unmapped:   unmapped,
			Note:       c.Note,
			CheckedAt:  c.CheckedAt.Format(time.RFC3339),
		})
	}
	return out
}

type uiFile struct {
	SourceID string            `json:"sourceId"`
	Path     string            `json:"path"`
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Records  int64             `json:"records"`
	Metadata map[string]string `json:"metadata"`
}

func toUIFile(v query.FileView) uiFile {
	return uiFile{
		SourceID: v.Identity.SourceID,
		Path:     v.Identity.Path,
		Name:     v.Identity.Name,
		Status:   v.Status,
		Records:  v.Records,
		Metadata: v.Metadata,
	}
}

func toUIFiles(views []query.FileView) []uiFile {
	out := make([]uiFile, 0, len(views))
	for _, v := range views {
		out = append(out, toUIFile(v))
	}
	return out
}

// uiFailure flattens the failure ledger record for the console: the
// application struct nests Key/File, the UI wants one flat camelCase row.
type uiFailure struct {
	SourceID string `json:"sourceId"`
	Date     string `json:"date"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error"`
	FailedAt string `json:"failedAt"`
}

func toUIFailures(views []model.FileFailure) []uiFailure {
	out := make([]uiFailure, 0, len(views))
	for _, f := range views {
		out = append(out, uiFailure{
			SourceID: string(f.Key.SourceID),
			Date:     f.Key.Date.String(),
			Name:     f.File.Name,
			Path:     f.File.Path,
			Attempts: f.Attempts,
			Error:    f.Error,
			FailedAt: f.FailedAt.Format(time.RFC3339),
		})
	}
	return out
}
