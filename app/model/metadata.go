package model

import "context"

// Metadata is the business interpretation of one discovered input file. It is
// deliberately independent from FileIdentity: Metadata answers "what does this
// file represent in the business?" while FileIdentity answers "which input
// file is this?".
//
// Metadata MUST NOT participate in FileIdentity.Identity(). Changing metadata
// rules must never make an already collected file look like a new file.
type Metadata struct {
	Values map[string]string
}

// NewMetadata returns an empty, usable Metadata value.
func NewMetadata() Metadata {
	return Metadata{Values: map[string]string{}}
}

// Get returns the value for key and whether the key is present. A missing key
// is semantically distinct from an empty value (see the path metadata spec).
func (m Metadata) Get(key string) (string, bool) {
	if m.Values == nil {
		return "", false
	}
	v, ok := m.Values[key]
	return v, ok
}

// Has reports whether key is present.
func (m Metadata) Has(key string) bool {
	_, ok := m.Get(key)
	return ok
}

// Len returns the number of present keys.
func (m Metadata) Len() int {
	return len(m.Values)
}

// Clone returns an independent copy of the metadata.
func (m Metadata) Clone() Metadata {
	out := NewMetadata()
	for k, v := range m.Values {
		out.Values[k] = v
	}
	return out
}

// Equal reports whether two Metadata values carry exactly the same keys and
// values.
func (m Metadata) Equal(o Metadata) bool {
	if len(m.Values) != len(o.Values) {
		return false
	}
	for k, v := range m.Values {
		ov, ok := o.Values[k]
		if !ok || ov != v {
			return false
		}
	}
	return true
}

// FileDescriptor is a discovered input file that has already received its
// business metadata interpretation.
//
//	FileIdentity + Metadata = FileDescriptor
type FileDescriptor struct {
	Identity FileIdentity
	Metadata Metadata
}

// MetadataExtractor is the Application Capability Contract for interpreting a
// discovered file into business metadata. It is NOT a GOCORDIS Runtime API:
// implementations live in this application and are provided through a GOCORDIS
// Component (PathMetadataPlugin).
type MetadataExtractor interface {
	Extract(ctx context.Context, file FileIdentity) (Metadata, error)
}
