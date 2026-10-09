package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func BundleDigest(files []File) string {
	ordered := append([]File(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	hash := sha256.New()
	for _, file := range ordered {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", file.Path, file.Digest, file.Bytes)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func (bundle Bundle) Validate() error {
	if bundle.SchemaVersion != "core-config/v2" {
		return fmt.Errorf("unsupported bundle schema version %q", bundle.SchemaVersion)
	}
	if strings.TrimSpace(bundle.Revision) == "" {
		return errors.New("bundle revision is required")
	}
	if !isSHA256(bundle.Digest) {
		return errors.New("bundle digest must be a sha256 digest")
	}
	if bundle.Digest != BundleDigest(bundle.Files) {
		return errors.New("bundle digest does not match files")
	}
	previous := ""
	for _, file := range bundle.Files {
		if file.Path == "" || file.Path <= previous || !isSHA256(file.Digest) || file.Bytes < 0 {
			return errors.New("bundle files must be sorted, unique and valid")
		}
		previous = file.Path
	}
	seenServices := make(map[string]struct{}, len(bundle.Services))
	for _, service := range bundle.Services {
		if service.ID == "" || !jsonObject(service.Settings) {
			return errors.New("bundle services require unique ids and JSON objects")
		}
		if _, ok := seenServices[service.ID]; ok {
			return fmt.Errorf("bundle contains duplicate service %q", service.ID)
		}
		seenServices[service.ID] = struct{}{}
	}
	for _, link := range bundle.Links {
		if !jsonObject(link) {
			return errors.New("bundle links must be JSON objects")
		}
	}
	return nil
}

func isSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func jsonObject(raw []byte) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && value != nil
}
