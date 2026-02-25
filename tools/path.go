package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// unicodeSpaces are Unicode space characters normalized to ASCII space.
var unicodeSpaces = []rune{
	'\u00A0', // NO-BREAK SPACE
	'\u2002', // EN SPACE
	'\u2003', // EM SPACE
	'\u2004', // THREE-PER-EM SPACE
	'\u2005', // FOUR-PER-EM SPACE
	'\u2006', // SIX-PER-EM SPACE
	'\u2007', // FIGURE SPACE
	'\u2008', // PUNCTUATION SPACE
	'\u2009', // THIN SPACE
	'\u200A', // HAIR SPACE
	'\u202F', // NARROW NO-BREAK SPACE
	'\u205F', // MEDIUM MATHEMATICAL SPACE
	'\u3000', // IDEOGRAPHIC SPACE
}

// ExpandPath normalizes a user-provided path:
//   - Replaces Unicode special spaces with ASCII space
//   - Expands ~ to the user's home directory
func ExpandPath(p string) string {
	for _, r := range unicodeSpaces {
		p = strings.ReplaceAll(p, string(r), " ")
	}

	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// ResolvePath resolves a user-provided path against a working directory.
// If userPath is empty, returns workDir. If absolute, returns as-is.
// Otherwise joins with workDir.
func ResolvePath(workDir, userPath string) string {
	if userPath == "" {
		return workDir
	}
	expanded := ExpandPath(userPath)
	if filepath.IsAbs(expanded) {
		return expanded
	}
	return filepath.Join(workDir, expanded)
}

// skipDirs are directory names excluded from recursive traversal.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
	".venv":        true,
}

// IsSkipDir reports whether a directory name should be excluded from traversal.
func IsSkipDir(name string) bool {
	return skipDirs[name]
}
