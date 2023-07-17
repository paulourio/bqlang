// Package extensions has a grammar definition that can be seen an as
// extension o ZetaSQL's.
// The official ZetaSQL parser ignores white spaces and comments,
// and this additional parser assists on tracking newlines and comments.
// The grammar also parses Jinja2-like templates, so that we can format
// simple templated-queries.

package extensions
