// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Abdul Ghani (VIKOIT)

package domain

// ChangeRef identifies the change under analysis: a commit SHA or a pull-request ref.
//
// It is opaque to the engine. Only the FileProvider that resolves it knows how to interpret it,
// which is what keeps the analyzers free of any notion of git or GitHub.
type ChangeRef string

// ChangeStatus describes what happened to a file in a changeset.
type ChangeStatus string

// The complete set of change statuses. Anything outside this set is a programming error, not an
// input to classify.
const (
	StatusAdded    ChangeStatus = "ADDED"
	StatusModified ChangeStatus = "MODIFIED"
	StatusRemoved  ChangeStatus = "REMOVED"
	StatusRenamed  ChangeStatus = "RENAMED"
)

// Valid reports whether s is one of the defined statuses.
func (s ChangeStatus) Valid() bool {
	switch s {
	case StatusAdded, StatusModified, StatusRemoved, StatusRenamed:
		return true
	default:
		return false
	}
}

// ChangedFile is one file in a changeset, carrying both sides of the change.
//
// Both sides are required because reversibility is a property of a transition, not of a file:
// whether an ALTER widens or narrows a column, and whether a PVC grew or shrank, is unanswerable
// from the new content alone. Providers are responsible for populating both.
type ChangedFile struct {
	// Path is the repository-relative path after the change, using forward slashes on every
	// platform so that certificates do not differ between Windows and Linux.
	Path string `json:"path"`

	// PreviousPath is set only for StatusRenamed; empty otherwise.
	PreviousPath string `json:"previousPath,omitempty"`

	Status ChangeStatus `json:"status"`

	// Previous is the content before the change. Nil for StatusAdded.
	Previous []byte `json:"-"`

	// Current is the content after the change. Nil for StatusRemoved.
	Current []byte `json:"-"`
}

// IsAdded reports whether the file did not exist before this change.
func (f ChangedFile) IsAdded() bool { return f.Status == StatusAdded }

// IsRemoved reports whether the file does not exist after this change.
func (f ChangedFile) IsRemoved() bool { return f.Status == StatusRemoved }
