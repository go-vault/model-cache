package hub

import (
	"path/filepath"
	"strings"
)


func filterFilesByPattern(files []string, allowPatterns []string, ignorePatterns []string) []string {
	if len(allowPatterns) == 0 && len(ignorePatterns) == 0 {
		return files
	}

	var filtered []string
	for _, file := range files{
		// skip if matches ignore patterns
		if matchesAnyPattern(file, ignorePatterns) {
			continue
		}

		// include if no allow patterns or matches any allow pattern
		if len(allowPatterns) == 0 || matchesAnyPattern(file, allowPatterns) {
			filtered = append(filtered, file)
		}
	}

	return filtered
}


func matchesAnyPattern(file string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}

	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, file)
		if err == nil && matched {
			return true
		}

		// handle directory wildcard pattern (e.g., "directory/*")
		if strings.Contains(pattern, "/") {
			matched, err = filepath.Match(pattern, filepath.ToSlash(file))
			if err == nil && matched {
				return true
			}
		}
	}

	return false
}
