package query

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gocordis-csv-collector/app/model"
)

func key(source, date string) model.CollectionKey {
	var d model.CollectionDate
	_ = d.UnmarshalText([]byte(date))
	return model.CollectionKey{SourceID: model.SourceID(source), Date: d}
}

func file(source, path, name string) model.FileIdentity {
	return model.FileIdentity{SourceID: model.SourceID(source), Path: path, Name: name}
}

func TestReadModelBuildsViewsFromEvents(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	k := key("src-a", "2026-09-06")
	f := file("src-a", "/data/line-A/station-01/product-A.csv", "product-A.csv")
	md := model.Metadata{Values: map[string]string{"line": "line-A", "station": "station-01", "product": "product-A"}}
	m.OnFileCompleted(k, f, md, 2)
	m.OnCollectionCompleted(k, time.Now())

	cols, err := m.ListCollections(ctx)
	if err != nil || len(cols) != 1 {
		t.Fatalf("collections = %v, err %v", cols, err)
	}
	c := cols[0]
	if c.Status != StatusSucceeded || c.FilesTotal != 1 || c.FilesCompleted != 1 || c.Records != 2 {
		t.Fatalf("collection view = %#v", c)
	}
	if c.SourceID != "src-a" || c.Date != "2026-09-06" {
		t.Fatalf("collection identity = %#v", c)
	}
	files, err := m.ListFiles(ctx, FileQueryRequest{SourceID: "src-a", Date: k.Date})
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v, err %v", files, err)
	}
	if files[0].Metadata["product"] != "product-A" || files[0].Status != StatusSucceeded {
		t.Fatalf("file view = %#v", files[0])
	}
	got, err := m.GetFileMetadata(ctx, f)
	v, ok := got.Get("line")
	if err != nil || !ok || v != "line-A" {
		t.Fatalf("metadata = %v, err %v", got, err)
	}
}

func TestReadModelTracksFailuresAndSources(t *testing.T) {
	ctx := context.Background()
	m := NewReadModel()
	k := key("src-a", "2026-09-06")
	m.OnFileCompleted(k, file("src-a", "/a.csv", "a.csv"), model.Metadata{}, 1)
	m.OnFileFailed(k, file("src-a", "/b.csv", "b.csv"), model.Metadata{}, 0, "boom")
	m.OnCollectionFailed(k, "boom")
	col, err := m.GetCollection(ctx, k)
	if err != nil || col.Status != StatusFailed || col.FilesTotal != 2 || col.FilesCompleted != 1 || col.FilesFailed != 1 {
		t.Fatalf("collection = %#v err %v", col, err)
	}
	srcs, _ := m.ListSources(ctx)
	if len(srcs) != 1 || srcs[0].ID != "src-a" {
		t.Fatalf("sources = %#v", srcs)
	}
	if _, err := m.GetCollection(ctx, key("nope", "2026-09-06")); err == nil {
		t.Fatal("missing collection must error")
	}
}

func TestObservationSubscribePublishAndFeed(t *testing.T) {
	s := NewObservationService()
	var n atomic.Int32
	unsub, err := s.Subscribe(context.Background(), func(ObservationEvent) { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	s.Publish(ObservationEvent{Type: "FileCompleted", Key: key("s", "2026-09-06")})
	if n.Load() != 1 {
		t.Fatalf("subscriber called %d times", n.Load())
	}
	_ = unsub()
	s.Publish(ObservationEvent{Type: "CollectionCompleted", Key: key("s", "2026-09-06")})
	if n.Load() != 1 {
		t.Fatalf("subscriber must stop after unsubscribe")
	}
	feed := s.Latest(10)
	if len(feed) != 2 {
		t.Fatalf("feed = %#v", feed)
	}
}
