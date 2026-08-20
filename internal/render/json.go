package render

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
	"github.com/VIKOIT/reversibility-engine/pkg/certificate"
)

// JSON renders the certificate as the public wire schema.
//
// It converts to pkg/certificate rather than marshalling the internal type directly. That is the
// whole point of the public package: what this writes is a contract external consumers can pin,
// and refactoring the internal model must not silently change it.
type JSON struct{}

// Format implements Renderer.
func (JSON) Format() string { return FormatJSON }

// Render implements Renderer.
func (JSON) Render(w io.Writer, cert domain.ReversibilityCertificate) error {
	enc := json.NewEncoder(w)

	// Indented because humans read this in CI logs as often as machines parse it, and a diff
	// of two single-line JSON blobs is unreadable.
	enc.SetIndent("", "  ")

	// Struct field order is fixed by the type, and every slice is non-nil by the time it gets
	// here, so this encoding is deterministic without further sorting.
	if err := enc.Encode(certificate.FromDomain(cert)); err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	return nil
}
