package applyqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

const maxJournalBytes = 64 << 10

type Journal struct{ Directory string }

var _ domain.ApplyRecoveryStore = Journal{}

func Default() (Journal, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return Journal{}, fmt.Errorf("resolve application data directory: %w", err)
	}
	return Journal{Directory: filepath.Join(root, "liapoldus", "apply-queue", "v1")}, nil
}

// TargetKey binds serialization to one target endpoint without persisting its
// URL, which may contain operator-specific information.
func TargetKey(target, endpoint string) string {
	sum := sha256.Sum256([]byte(target + "\x00" + endpoint))
	return hex.EncodeToString(sum[:])
}

func (journal Journal) Acquire(ctx context.Context, targetKey string) (func() error, error) {
	if !validTargetKey(targetKey) || journal.Directory == "" {
		return nil, errors.New("invalid apply queue target")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(journal.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("create apply queue directory: %w", err)
	}
	if err := os.Chmod(journal.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("secure apply queue directory: %w", err)
	}
	file, err := os.OpenFile(journal.path(targetKey)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open apply queue lock: %w", err)
	}
	for {
		locked, lockErr := tryLock(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire apply queue lock: %w", lockErr)
		}
		if locked {
			return func() error {
				unlockErr := unlockFile(file)
				closeErr := file.Close()
				return errors.Join(unlockErr, closeErr)
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("waiting for another apply on this target: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (journal Journal) Load(targetKey string) (domain.ApplyIntent, bool, error) {
	if !validTargetKey(targetKey) || journal.Directory == "" {
		return domain.ApplyIntent{}, false, errors.New("invalid apply queue target")
	}
	file, err := os.Open(journal.path(targetKey) + ".json")
	if errors.Is(err, os.ErrNotExist) {
		return domain.ApplyIntent{}, false, nil
	}
	if err != nil {
		return domain.ApplyIntent{}, false, fmt.Errorf("open apply recovery journal: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxJournalBytes {
		return domain.ApplyIntent{}, false, errors.New("apply recovery journal is invalid or oversized")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxJournalBytes))
	decoder.DisallowUnknownFields()
	var intent domain.ApplyIntent
	if err := decoder.Decode(&intent); err != nil || !validIntent(intent) {
		return domain.ApplyIntent{}, false, errors.New("apply recovery journal is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domain.ApplyIntent{}, false, errors.New("apply recovery journal has trailing data")
	}
	return intent, true, nil
}

func (journal Journal) Save(targetKey string, intent domain.ApplyIntent) error {
	if !validTargetKey(targetKey) || journal.Directory == "" || !validIntent(intent) {
		return errors.New("invalid apply recovery intent")
	}
	if err := os.MkdirAll(journal.Directory, 0o700); err != nil {
		return fmt.Errorf("create apply queue directory: %w", err)
	}
	if err := os.Chmod(journal.Directory, 0o700); err != nil {
		return fmt.Errorf("secure apply queue directory: %w", err)
	}
	body, err := json.Marshal(intent)
	if err != nil || len(body) > maxJournalBytes {
		return errors.New("apply recovery intent exceeds the storage limit")
	}
	temporary, err := os.CreateTemp(journal.Directory, ".apply-queue-*.tmp")
	if err != nil {
		return fmt.Errorf("create apply recovery journal: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure apply recovery journal: %w", err)
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write apply recovery journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync apply recovery journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close apply recovery journal: %w", err)
	}
	if err := replaceFile(temporaryPath, journal.path(targetKey)+".json"); err != nil {
		return fmt.Errorf("publish apply recovery journal: %w", err)
	}
	return syncDirectory(journal.Directory)
}

func (journal Journal) Clear(targetKey string) error {
	if !validTargetKey(targetKey) || journal.Directory == "" {
		return errors.New("invalid apply queue target")
	}
	err := os.Remove(journal.path(targetKey) + ".json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clear apply recovery journal: %w", err)
	}
	return syncDirectory(journal.Directory)
}

func (journal Journal) path(targetKey string) string {
	return filepath.Join(journal.Directory, targetKey)
}

func validTargetKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validIntent(intent domain.ApplyIntent) bool {
	if strings.TrimSpace(intent.Scope) != intent.Scope || intent.Scope == "" || len(intent.Scope) > 1024 || len(intent.Keys) == 0 || len(intent.Keys) > 64 {
		return false
	}
	seen := make(map[string]struct{}, len(intent.Keys))
	for _, key := range intent.Keys {
		if strings.TrimSpace(key) != key || key == "" || len(key) > 512 {
			return false
		}
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}
