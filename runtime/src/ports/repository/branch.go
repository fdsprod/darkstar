package repository

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

const maxBranchComponentLength = 80

// BranchName deterministically derives the default DARKSTAR branch name from a
// human-facing source key or slug and the immutable work-item ID.
func BranchName(sourceKey, workItemID string) (string, error) {
	sourceKey = strings.TrimSpace(sourceKey)
	workItemID = strings.TrimSpace(workItemID)
	if sourceKey == "" || workItemID == "" {
		return "", errors.New("source key and work item ID are required")
	}
	slug := slugComponent(sourceKey)
	if slug == "" {
		slug = "work"
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(workItemID)))[:10]
	maximumSlug := maxBranchComponentLength - len(digest) - 1
	if len(slug) > maximumSlug {
		slug = strings.Trim(slug[:maximumSlug], "-.")
	}
	return "darkstar/" + slug + "-" + digest, nil
}

func slugComponent(value string) string {
	var result strings.Builder
	separator := false
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			result.WriteRune(character)
			separator = false
			continue
		}
		if !separator && result.Len() > 0 {
			result.WriteByte('-')
			separator = true
		}
	}
	return strings.Trim(result.String(), "-.")
}
