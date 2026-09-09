// Wire DTOs of the collector console. They are the camelCase shapes the
// frontend consumes; the bridge maps Application views onto them so the
// platform hub stays domain-free and the client contract stays stable.
package consolebridge

import (
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
	return uiSource{ID: v.ID, Name: v.ID, Path: v.Path, Profiles: v.Profiles, Status: v.Status}
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
