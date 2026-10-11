package localcore

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Liapoldus/cli/internal/domain"
)

// StateFiles provides offline backup and restore for a stopped Docker Core.
// It never starts, signals, or inspects a host Core process.
type StateFiles struct{}

func (StateFiles) Backup(target domain.Target, output string) error {
	if target.Transport != domain.TransportLocalProcess {
		return errors.New("Core state backup requires a Docker runtime target")
	}
	if target.StatePath == "" || !filepath.IsAbs(target.StatePath) {
		return errors.New("target does not declare an absolute Core state path")
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return copyFile(target.StatePath, output, false)
}

func (StateFiles) Restore(target domain.Target, input string) error {
	if target.Transport != domain.TransportLocalProcess {
		return errors.New("Core state restore requires a Docker runtime target")
	}
	if target.StatePath == "" || !filepath.IsAbs(target.StatePath) {
		return errors.New("target does not declare an absolute Core state path")
	}
	info, err := os.Stat(input)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("backup input must be a regular file")
	}
	temporary := target.StatePath + ".restore.tmp"
	if err := copyFile(input, temporary, false); err != nil {
		return err
	}
	if err := os.Rename(temporary, target.StatePath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func copyFile(source, destination string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Lstat(destination); err == nil {
			return errors.New("destination already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}
