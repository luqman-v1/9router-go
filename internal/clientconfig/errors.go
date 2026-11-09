package clientconfig

import "errors"

// Errors of the patch layer. They are separate from the path/write sentinels in
// safefile.go because they describe the *content* of a file rather than the way
// it is reached or written, and because every one of them is raised before the
// caller creates a backup or touches the original.
var (
	// ErrParseFailure reports a lexer or parser error in the user's file, or a
	// semantic error the format's own validator reports (duplicate TOML key or
	// table). The file is left exactly as it was.
	ErrParseFailure = errors.New("clientconfig: config parse failure")

	// ErrUnsupportedStructure reports valid input containing a shape this
	// patcher will not splice: a TOML inline table, array, or multi-line
	// string; a YAML block scalar, alias, or non-scalar target. Refusing is the
	// point — a partial rewrite of such a value would silently change meaning.
	ErrUnsupportedStructure = errors.New("clientconfig: unsupported structure")

	// ErrKeyNotFound reports a key path that does not exist and that the caller
	// did not ask to create.
	ErrKeyNotFound = errors.New("clientconfig: key not found")

	// ErrAlreadyPatched reports that the patch would not change a single byte,
	// so nothing needs to be written. It is returned alongside the unchanged
	// bytes; callers treat it as success (see docs/specs/config-patch-spec.md
	// §6). It is a sentinel rather than a bool so a caller can log the
	// difference between "changed" and "already correct".
	ErrAlreadyPatched = errors.New("clientconfig: already patched")

	// ErrInternalInconsistency reports that output produced by this package
	// failed to re-parse. That is a bug in 9Router, never a user error, and
	// nothing is returned for writing.
	ErrInternalInconsistency = errors.New("clientconfig: patch produced unparsable output")
)
