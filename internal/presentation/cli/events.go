package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

const studioEventsFlag = "--studio-events"
const dockerMountUnavailableCode = "docker_mount_source_unavailable"
const serviceReplicaLeaseExpiredCode = "service_replica_lease_expired"

var studioAbsolutePathPattern = regexp.MustCompile(`(^|[\s=])(/[^\s"'<>]+|[A-Za-z]:\\[^\s"'<>]+)`)

type studioEvent struct {
	SchemaVersion string          `json:"schemaVersion"`
	Type          string          `json:"type"`
	RunID         string          `json:"runId,omitempty"`
	Severity      string          `json:"severity,omitempty"`
	Code          string          `json:"code,omitempty"`
	Message       string          `json:"message,omitempty"`
	OperationID   string          `json:"operationId,omitempty"`
	Target        string          `json:"target,omitempty"`
	State         string          `json:"state,omitempty"`
	Phase         string          `json:"phase,omitempty"`
	ReportPath    string          `json:"reportPath,omitempty"`
	Digest        string          `json:"digest,omitempty"`
	Percent       int             `json:"percent,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	ExitCode      *int            `json:"exitCode,omitempty"`
}

// studioEventWriter turns the CLI's existing one-line JSON result surface into
// the versioned stream consumed by Studio. Stderr is reduced to a bounded,
// credential-redacted diagnostic. Plain text stdout (used by typed log/inspect
// commands) is also bounded and redacted before it reaches the renderer.
type studioEventWriter struct {
	output io.Writer
	runID  string
	kind   string
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (writer *studioEventWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if _, err := writer.buf.Write(data); err != nil {
		return 0, err
	}
	for {
		index := bytes.IndexByte(writer.buf.Bytes(), '\n')
		if index < 0 {
			return len(data), nil
		}
		line := string(writer.buf.Next(index + 1))
		if writeErr := writer.emitLine(line); writeErr != nil {
			return len(data), writeErr
		}
	}
}

func (writer *studioEventWriter) Flush() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.buf.Len() == 0 {
		return nil
	}
	line := writer.buf.String()
	writer.buf.Reset()
	return writer.emitLine(line)
}

func (writer *studioEventWriter) emitLine(line string) error {
	if writer.kind == "stderr" {
		code, message := studioDiagnosticFields(line)
		severity := "warning"
		if code != "cli.stderr" {
			severity = "error"
		}
		return emitStudioEvent(writer.output, studioEvent{
			SchemaVersion: "cli-events/v1",
			Type:          "diagnostic",
			RunID:         writer.runID,
			Severity:      severity,
			Code:          code,
			Message:       message,
		})
	}
	line = string(bytes.TrimSpace([]byte(line)))
	if line == "" {
		return nil
	}
	if !json.Valid([]byte(line)) {
		if writer.kind == "stdout" {
			payload, err := json.Marshal(map[string]string{"text": safeStudioDiagnostic(line)})
			if err != nil {
				return err
			}
			return emitStudioEvent(writer.output, studioEvent{
				SchemaVersion: "cli-events/v1",
				Type:          "run.output",
				RunID:         writer.runID,
				Payload:       payload,
			})
		}
		return emitStudioEvent(writer.output, studioEvent{
			SchemaVersion: "cli-events/v1",
			Type:          "diagnostic",
			RunID:         writer.runID,
			Severity:      "error",
			Code:          "cli.protocol",
			Message:       "liapoldus emitted non-JSON output",
		})
	}
	var event studioEvent
	if json.Unmarshal([]byte(line), &event) == nil && event.SchemaVersion == "cli-events/v1" && event.Type != "" {
		if event.RunID == "" {
			event.RunID = writer.runID
		}
		return emitStudioEvent(writer.output, event)
	}
	eventType := "run.output"
	var metadata struct {
		OperationID string `json:"operationId"`
		Target      string `json:"target"`
		State       string `json:"state"`
		Phase       string `json:"phase"`
		ReportPath  string `json:"reportPath"`
		Digest      string `json:"digest"`
		Percent     int    `json:"percent"`
	}
	_ = json.Unmarshal([]byte(line), &metadata)
	if metadata.ReportPath != "" {
		eventType = "report.available"
	} else if metadata.OperationID != "" && metadata.State != "" {
		eventType = "operation.updated"
	}
	return emitStudioEvent(writer.output, studioEvent{
		SchemaVersion: "cli-events/v1",
		Type:          eventType,
		RunID:         writer.runID,
		OperationID:   metadata.OperationID,
		Target:        metadata.Target,
		State:         metadata.State,
		Phase:         metadata.Phase,
		ReportPath:    metadata.ReportPath,
		Digest:        metadata.Digest,
		Percent:       metadata.Percent,
		Payload:       json.RawMessage(line),
	})
}

func safeStudioDiagnostic(value string) string {
	message := strings.TrimSpace(value)
	if message == "" {
		return "liapoldus emitted a non-JSON diagnostic"
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "-----begin ") && strings.Contains(lower, "private key-----") {
		return "[redacted private key material]"
	}
	if index := strings.Index(lower, "bearer "); index >= 0 {
		message = message[:index+len("bearer ")] + "[redacted]"
	}
	for _, key := range []string{"token", "password", "secret", "private-key", "privatekey", "authorization"} {
		lower = strings.ToLower(message)
		for _, separator := range []string{"=", ":"} {
			marker := key + separator
			if index := strings.Index(lower, marker); index >= 0 {
				message = message[:index+len(marker)] + "[redacted]"
				lower = strings.ToLower(message)
			}
		}
	}
	message = studioAbsolutePathPattern.ReplaceAllString(message, "$1[redacted path]")
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}

func studioDiagnosticFields(value string) (string, string) {
	message := safeStudioDiagnostic(value)
	lower := strings.ToLower(message)
	if strings.Contains(lower, "service sdk replica lease expired") || strings.Contains(lower, "replica_lease_expired") {
		return serviceReplicaLeaseExpiredCode, "Core rejected the expired service replica incarnation. Issue a new operator-signed service identity/certificate before starting this service; retrying with the same certificate cannot recover it."
	}
	marker := dockerMountUnavailableCode + ": "
	if index := strings.Index(message, marker); index >= 0 &&
		(index == 0 || strings.ContainsRune(": \t", rune(message[index-1]))) {
		message = strings.TrimSpace(message[:index] + message[index+len(marker):])
		return dockerMountUnavailableCode, message
	}
	return "cli.stderr", message
}

func emitStudioEvent(output io.Writer, event studioEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = output.Write(append(data, '\n'))
	return err
}

func isStudioEventsRequest(args []string) (bool, []string) {
	clean := append([]string(nil), args...)
	for index, argument := range clean {
		if argument != studioEventsFlag {
			continue
		}
		clean = append(clean[:index], clean[index+1:]...)
		return true, clean
	}
	return false, clean
}

func studioRunID() string {
	return "studio-run-" + time.Now().UTC().Format("20060102T150405.000000000Z")
}
