// Package funcdocs embeds official BigQuery SQL function signatures, parameter
// documentation, usage descriptions, return types, and examples for bqpls.
package funcdocs

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

//go:embed bq_functions.json.gz
var embeddedFuncDocsGZ []byte

// ParameterDoc describes a single parameter within a function signature.
type ParameterDoc struct {
	Label         string `json:"label"`
	Documentation string `json:"documentation,omitempty"`
	Optional      bool   `json:"optional,omitempty"`
}

// SignatureDoc describes one call overload of a BigQuery function.
type SignatureDoc struct {
	Label         string         `json:"label"`
	Documentation string         `json:"documentation,omitempty"`
	Parameters    []ParameterDoc `json:"parameters,omitempty"`
	Variadic      bool           `json:"variadic,omitempty"`
}

// FuncDoc holds BigQuery reference documentation for a built-in function.
type FuncDoc struct {
	Name        string         `json:"name"`
	Category    string         `json:"category,omitempty"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	ReturnType  string         `json:"return_type,omitempty"`
	Examples    []string       `json:"examples,omitempty"`
	DocURL      string         `json:"doc_url,omitempty"`
	Signatures  []SignatureDoc `json:"signatures"`
}

type registry struct {
	all         []*FuncDoc
	byUpper     map[string]*FuncDoc
	byNamespace map[string][]*FuncDoc
}

var (
	regOnce sync.Once
	regData *registry
)

func loadRegistry() *registry {
	regOnce.Do(func() {
		regData = decodeEmbeddedDocs()
	})

	return regData
}

func decodeEmbeddedDocs() *registry {
	gz, err := gzip.NewReader(bytes.NewReader(embeddedFuncDocsGZ))
	if err != nil {
		panic(fmt.Errorf("funcdocs: open embedded gzip: %w", err))
	}
	defer func() { _ = gz.Close() }()

	var raw []*FuncDoc
	if err := json.NewDecoder(gz).Decode(&raw); err != nil {
		panic(fmt.Errorf("funcdocs: decode embedded json: %w", err))
	}

	byUpper := make(map[string]*FuncDoc, len(raw))
	byNS := make(map[string][]*FuncDoc)

	for _, doc := range raw {
		upper := strings.ToUpper(doc.Name)
		doc.Name = upper
		byUpper[upper] = doc

		if ns, _, hasDot := strings.Cut(upper, "."); hasDot {
			byNS[ns] = append(byNS[ns], doc)
		}
	}

	for ns := range byNS {
		slices.SortFunc(byNS[ns], func(a, b *FuncDoc) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	return &registry{
		all:         raw,
		byUpper:     byUpper,
		byNamespace: byNS,
	}
}

// Lookup returns the BigQuery documentation entry for name (case-insensitive).
func Lookup(name string) (*FuncDoc, bool) {
	if name == "" {
		return nil, false
	}

	doc, ok := loadRegistry().byUpper[strings.ToUpper(strings.TrimSpace(name))]

	return doc, ok
}

// LookupWithSafe resolves name (case-insensitive) and handles the BigQuery SAFE.
// error-suppression prefix (for example, SAFE.PARSE_DATE -> PARSE_DATE, isSafe=true).
func LookupWithSafe(name string) (doc *FuncDoc, isSafe, ok bool) {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if upper == "" {
		return nil, false, false
	}

	if d, found := loadRegistry().byUpper[upper]; found {
		return d, false, true
	}

	if rest, hasSafe := strings.CutPrefix(upper, "SAFE."); hasSafe {
		if d, found := loadRegistry().byUpper[rest]; found {
			return d, true, true
		}
	}

	return nil, false, false
}

// All returns a read-only slice of all documented BigQuery functions sorted by name.
func All() []*FuncDoc {
	return loadRegistry().all
}

// ByNamespace returns all documented BigQuery functions in namespace ns
// (case-insensitive, e.g. "AI", "ML", "NET", "KEYS", "AEAD", "HLL_COUNT", "OBJ").
func ByNamespace(ns string) []*FuncDoc {
	return loadRegistry().byNamespace[strings.ToUpper(strings.TrimSpace(ns))]
}

// PrimaryDetail returns a concise single-line signature summary suitable for
// LSP CompletionItem.Detail.
func PrimaryDetail(doc *FuncDoc) string {
	if doc == nil {
		return ""
	}

	if len(doc.Signatures) == 0 {
		return doc.Category
	}

	label := doc.Signatures[0].Label
	if len(label) > 96 {
		if idx := strings.IndexByte(label, '('); idx > 0 {
			label = label[:idx] + "(...)"
		}
	}

	if len(doc.Signatures) > 1 {
		return fmt.Sprintf("%s (+%d overloads)", label, len(doc.Signatures)-1)
	}

	return label
}

// FormatHoverMarkdown formats full Markdown hover documentation for a BigQuery function.
// When resolvedSig is non-empty and differs from the doc's signatures, it is omitted
// if redundant or shown concisely if helpful.
func FormatHoverMarkdown(doc *FuncDoc, isSafe bool, resolvedSig string) string {
	if doc == nil {
		return ""
	}

	var b strings.Builder

	title := doc.Name
	if isSafe {
		title = "SAFE." + doc.Name
	}

	if doc.Category != "" {
		fmt.Fprintf(&b, "### Function `%s` *(%s)*\n", title, doc.Category)
	} else {
		fmt.Fprintf(&b, "### Function `%s`\n", title)
	}

	if isSafe {
		b.WriteString("\n> `SAFE.` prefix: returns `NULL` instead of raising an error on invalid input.\n")
	}

	if len(doc.Signatures) > 0 {
		b.WriteString("\n```sql\n")
		for i, sig := range doc.Signatures {
			if i > 0 {
				b.WriteByte('\n')
			}
			if isSafe && !strings.HasPrefix(strings.ToUpper(sig.Label), "SAFE.") {
				b.WriteString("SAFE.")
			}
			b.WriteString(sig.Label)
		}
		b.WriteString("\n```\n")
	} else if resolvedSig != "" {
		fmt.Fprintf(&b, "\n```sql\n%s\n```\n", resolvedSig)
	}

	if doc.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", doc.Description)
	} else if doc.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", doc.Summary)
	}

	writeReturnAndExamples(&b, doc)

	if doc.DocURL != "" {
		fmt.Fprintf(&b, "\n[BigQuery Documentation](%s)\n", doc.DocURL)
	}

	return strings.TrimSpace(b.String())
}

// FormatSignatureDoc formats Markdown documentation for LSP SignatureInformation.Documentation.
func FormatSignatureDoc(doc *FuncDoc, sig *SignatureDoc, isSafe bool) string {
	if doc == nil {
		return ""
	}

	var b strings.Builder

	if isSafe {
		b.WriteString("> `SAFE.` prefix: returns `NULL` instead of raising an error on invalid input.\n\n")
	}

	if sig != nil && sig.Documentation != "" {
		b.WriteString(sig.Documentation)
		b.WriteString("\n\n")
	}

	if doc.Description != "" {
		b.WriteString(doc.Description)
		b.WriteByte('\n')
	} else if doc.Summary != "" {
		b.WriteString(doc.Summary)
		b.WriteByte('\n')
	}

	writeReturnAndExamples(&b, doc)

	if doc.DocURL != "" {
		fmt.Fprintf(&b, "\n[BigQuery Documentation](%s)\n", doc.DocURL)
	}

	return strings.TrimSpace(b.String())
}

// FormatCompletionDoc formats concise Markdown documentation for LSP CompletionItem.Documentation.
func FormatCompletionDoc(doc *FuncDoc) string {
	if doc == nil {
		return ""
	}

	var b strings.Builder

	if len(doc.Signatures) > 0 {
		b.WriteString("```sql\n")
		maxSigs := min(len(doc.Signatures), 3)
		for i := range maxSigs {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(doc.Signatures[i].Label)
		}
		if len(doc.Signatures) > maxSigs {
			fmt.Fprintf(&b, "\n-- ... +%d more overloads", len(doc.Signatures)-maxSigs)
		}
		b.WriteString("\n```\n\n")
	}

	if doc.Summary != "" {
		b.WriteString(doc.Summary)
		b.WriteByte('\n')
	}

	if doc.ReturnType != "" {
		fmt.Fprintf(&b, "\n**Returns:** %s\n", doc.ReturnType)
	}

	if len(doc.Examples) > 0 {
		fmt.Fprintf(&b, "\n```sql\n%s\n```\n", doc.Examples[0])
	}

	return strings.TrimSpace(b.String())
}

func writeReturnAndExamples(b *strings.Builder, doc *FuncDoc) {
	if doc.ReturnType != "" {
		fmt.Fprintf(b, "\n**Returns:** %s\n", doc.ReturnType)
	}

	if len(doc.Examples) > 0 {
		b.WriteString("\n**Example:**\n```sql\n")
		b.WriteString(doc.Examples[0])
		b.WriteString("\n```\n")
	}
}
