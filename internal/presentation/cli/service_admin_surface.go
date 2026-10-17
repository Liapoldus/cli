package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const maximumServiceAdminSurfaceBytes = 1 << 20

// validServiceAdminSurface checks the generic page/section/action shape without
// interpreting product-owned fields. Core remains the contract validator and
// owner of live SDK access.
func validServiceAdminSurface(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > maximumServiceAdminSurfaceBytes {
		return false
	}
	var document struct {
		Pages []struct {
			ID       string `json:"id"`
			Sections []struct {
				ID      string `json:"id"`
				Actions []struct {
					ID string `json:"id"`
				} `json:"actions"`
			} `json:"sections"`
		} `json:"pages"`
	}
	if json.Unmarshal(raw, &document) != nil || len(document.Pages) == 0 || len(document.Pages) > 128 {
		return false
	}
	pageIDs := make(map[string]bool, len(document.Pages))
	for _, page := range document.Pages {
		if !safeServiceResourceID(page.ID) || pageIDs[page.ID] || len(page.Sections) == 0 || len(page.Sections) > 128 {
			return false
		}
		pageIDs[page.ID] = true
		sectionIDs := make(map[string]bool, len(page.Sections))
		for _, section := range page.Sections {
			if !safeServiceResourceID(section.ID) || sectionIDs[section.ID] || len(section.Actions) > 128 {
				return false
			}
			sectionIDs[section.ID] = true
			actionIDs := make(map[string]bool, len(section.Actions))
			for _, action := range section.Actions {
				if !safeServiceResourceID(action.ID) || actionIDs[action.ID] {
					return false
				}
				actionIDs[action.ID] = true
			}
		}
	}
	return true
}

func validServiceAdminSurfaceDigest(digest string, descriptor json.RawMessage) bool {
	if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") || len(descriptor) == 0 || len(descriptor) > maximumServiceAdminSurfaceBytes {
		return false
	}
	encoded := digest[len("sha256:"):]
	if strings.ToLower(encoded) != encoded {
		return false
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return false
	}
	actual := sha256.Sum256(descriptor)
	return hex.EncodeToString(actual[:]) == encoded
}
