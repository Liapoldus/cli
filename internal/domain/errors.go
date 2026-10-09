package domain

import (
	"errors"
	"strconv"
)

var (
	ErrInvalidRevision     = errors.New("revision is not a Git commit")
	ErrDirtyWorkingTree    = errors.New("working tree is not clean")
	ErrTargetNotConfigured = errors.New("target is not configured")
)

type CoreHTTPError struct {
	Operation string
	Status    int
}

func (e CoreHTTPError) Error() string {
	return "core " + e.Operation + " returned HTTP " + strconv.Itoa(e.Status)
}

type CoreContractError struct {
	Operation string
	Message   string
}

type ApprovalEvidence struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Target     string `json:"target"`
	Approved   bool   `json:"approved"`
	Source     string `json:"source"`
	EvidenceID string `json:"evidenceId"`
}

func (e ApprovalEvidence) Validate(repository, revision, target string) error {
	if !e.Approved || e.Repository == "" || e.Revision == "" || e.Target == "" || e.Repository != repository || e.Revision != revision || e.Target != target || e.Source == "" || e.EvidenceID == "" {
		return errors.New("approval evidence does not match repository, revision, target or approval policy")
	}
	return nil
}

func (e CoreContractError) Error() string {
	return "invalid core " + e.Operation + " response: " + e.Message
}
