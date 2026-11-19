// Package extensions provides a lexical and syntactic scanner extension for
// GoogleSQL scripts.
//
// Because the official GoogleSQL parser discards whitespace and comments and
// does not recognize Jinja2 templates, this auxiliary parser extracts comment
// spans and Jinja2 template elements while preserving exact byte offsets.
package extensions
