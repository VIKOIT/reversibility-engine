package render

import (
	"fmt"
	"io"
	"sort"

	"github.com/VIKOIT/reversibility-engine/internal/domain"
)

// Renderer serializes a certificate for one consumer.
//
// Renderers are total functions over the certificate and add nothing to it. In particular they
// never re-derive a grade or a gate status: a second definition of the gate would be a second
// chance to get it wrong in the permissive direction, so they print what the engine decided.
type Renderer interface {
	// Format is the name used to select this renderer on the command line.
	Format() string

	// Render writes the certificate to w.
	//
	// Output must be deterministic: the same certificate produces byte-identical output every
	// time, because these are the bytes people diff and machines compare.
	Render(w io.Writer, cert domain.ReversibilityCertificate) error
}

// The formats a certificate can be rendered as.
const (
	FormatJSON     = "json"
	FormatMarkdown = "markdown"
	FormatSARIF    = "sarif"
)

// For returns the renderer for a format name.
func For(format string) (Renderer, error) {
	switch format {
	case FormatJSON:
		return JSON{}, nil
	case FormatMarkdown:
		return Markdown{}, nil
	case FormatSARIF:
		return SARIF{}, nil
	default:
		return nil, fmt.Errorf("unknown output format %q: want one of %v", format, Formats())
	}
}

// Formats lists the supported format names, sorted so help text does not shuffle.
func Formats() []string {
	formats := []string{FormatJSON, FormatMarkdown, FormatSARIF}
	sort.Strings(formats)
	return formats
}
