package extensions

import "errors"

var (
	// ErrInvalidComment is returned when a comment has an invalid structure.
	ErrInvalidComment = errors.New("invalid comment")
	// ErrParseExtension is returned when the extension scanner/parser fails.
	ErrParseExtension = errors.New("extension parse error")
)
