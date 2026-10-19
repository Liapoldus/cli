package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

var safeTargetReferenceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type coreSettingsDocument struct {
	Management struct {
		Listen         string            `json:"listen"`
		TLS            coreTLSReferences `json:"tls"`
		MaxBodyBytes   int64             `json:"maxBodyBytes"`
		HeaderTimeout  string            `json:"headerTimeout"`
		RequestTimeout string            `json:"requestTimeout"`
	} `json:"management"`
	ServiceControl struct {
		Listen            string            `json:"listen"`
		PublicURL         string            `json:"publicURL"`
		TLS               coreTLSReferences `json:"tls"`
		ReplicaClientCA   string            `json:"replicaClientCA"`
		ReplicaServerCA   string            `json:"replicaServerCA"`
		ReplicaClientCRLs []string          `json:"replicaClientCRLs,omitempty"`
		ReplicaServerCRLs []string          `json:"replicaServerCRLs,omitempty"`
	} `json:"serviceControl"`
	SecretRoot        string `json:"secretRoot"`
	TrafficController *struct {
		SchemaVersion     int      `json:"schemaVersion"`
		Listen            string   `json:"listen"`
		Certificate       string   `json:"certificate"`
		Key               string   `json:"key"`
		ClientCA          string   `json:"clientCA"`
		ClientCRLs        []string `json:"clientCRLs,omitempty"`
		AllowedIdentities []struct {
			CommonName string `json:"commonName"`
			URI        string `json:"uri"`
		} `json:"allowedIdentities"`
	} `json:"trafficController,omitempty"`
}

type coreTLSReferences struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
	ClientCA    string `json:"clientCA,omitempty"`
}

type coreFileReferenceView struct {
	ReferenceID string `json:"referenceId,omitempty"`
	Configured  bool   `json:"configured"`
	Available   bool   `json:"available"`
	State       string `json:"state"`
}

func (r Runner) coreSettings(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "update") {
		return fail(stderr, application.ExitArguments, "usage: liapoldus core settings inspect|update --target local")
	}
	options, err := parseOptions(args[1:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target != "local" {
		return fail(stderr, application.ExitArguments, "Core settings operations require --target local")
	}
	registry := r.registry(options.targets)
	if registry == nil {
		return fail(stderr, application.ExitTarget, "target registry is not configured")
	}
	target, err := registry.Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	client, err := r.runtimeCoreClient(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	settingsReader, ok := client.(domain.CoreSettingsReader)
	if !ok {
		return fail(stderr, application.ExitTarget, "Core client does not support typed settings inspection")
	}
	snapshot, err := settingsReader.ReadCoreSettings(ctx)
	if err != nil {
		return fail(stderr, coreSettingsErrorCode(err), err.Error())
	}
	if args[0] == "inspect" {
		view, err := projectCoreSettings(target, snapshot)
		if err != nil {
			return fail(stderr, application.ExitValidation, err.Error())
		}
		return encode(stdout, view)
	}
	if !options.confirm || options.requestFile == "" {
		return fail(stderr, application.ExitArguments, "core settings update requires --request-file and --confirm")
	}
	if snapshot.Pending || snapshot.RestartRequired {
		return fail(stderr, application.ExitConflict, "Core settings already have a pending revision; inspect or restart Core before another update")
	}
	patch, err := readCoreSettingsPatch(options.requestFile)
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	if patch.ExpectedRevision != snapshot.DesiredRevision {
		return fail(stderr, application.ExitConflict, "Core settings revision changed; inspect the latest settings before editing")
	}
	document, err := mergeCoreSettingsPatch(snapshot.Settings, patch)
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	settingsWriter, ok := client.(domain.CoreSettingsWriter)
	if !ok {
		return fail(stderr, application.ExitTarget, "Core client does not support typed settings updates")
	}
	updated, err := settingsWriter.UpdateCoreSettings(ctx, snapshot.ETag, document)
	if err != nil {
		return fail(stderr, coreSettingsErrorCode(err), err.Error())
	}
	view, err := projectCoreSettings(target, updated)
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	return encode(stdout, map[string]any{
		"schemaVersion": "core-settings-update-result/v1", "target": target.Name,
		"previousRevision": snapshot.DesiredRevision, "result": view,
	})
}

func readCoreSettingsPatch(path string) (domain.CoreSettingsPatch, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return domain.CoreSettingsPatch{}, errors.New("core settings request file must be an absolute local file")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > 32768 {
		return domain.CoreSettingsPatch{}, errors.New("core settings request file is missing, linked, or outside the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return domain.CoreSettingsPatch{}, errors.New("core settings request file cannot be read")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 32769))
	if err != nil || len(raw) > 32768 {
		return domain.CoreSettingsPatch{}, errors.New("core settings request exceeds the size limit")
	}
	var patch domain.CoreSettingsPatch
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&patch) != nil || decoder.Decode(new(any)) != io.EOF || patch.SchemaVersion != "core-settings-patch/v1" || patch.ExpectedRevision < 1 || !hasCoreSettingsPatch(patch) {
		return domain.CoreSettingsPatch{}, errors.New("core settings request does not match core-settings-patch/v1")
	}
	if patch.Management.MaxBodyBytes != nil && (*patch.Management.MaxBodyBytes < 1 || *patch.Management.MaxBodyBytes > 16<<20) {
		return domain.CoreSettingsPatch{}, errors.New("management.maxBodyBytes is outside the supported range")
	}
	for _, value := range []*string{patch.Management.Listen, patch.Management.HeaderTimeout, patch.Management.RequestTimeout,
		patch.ServiceControl.Listen, patch.ServiceControl.PublicURL} {
		if value != nil && (strings.TrimSpace(*value) == "" || strings.ContainsAny(*value, "\x00\r\n")) {
			return domain.CoreSettingsPatch{}, errors.New("core settings patch contains an empty or unsafe value")
		}
	}
	return patch, nil
}

func hasCoreSettingsPatch(patch domain.CoreSettingsPatch) bool {
	return patch.Management.Listen != nil || patch.Management.MaxBodyBytes != nil || patch.Management.HeaderTimeout != nil ||
		patch.Management.RequestTimeout != nil || patch.ServiceControl.Listen != nil || patch.ServiceControl.PublicURL != nil
}

func mergeCoreSettingsPatch(raw json.RawMessage, patch domain.CoreSettingsPatch) (json.RawMessage, error) {
	var document coreSettingsDocument
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, domain.CoreContractError{Operation: "settings", Message: "unsupported current settings document"}
	}
	if value := patch.Management.Listen; value != nil {
		document.Management.Listen = *value
	}
	if value := patch.Management.MaxBodyBytes; value != nil {
		document.Management.MaxBodyBytes = *value
	}
	if value := patch.Management.HeaderTimeout; value != nil {
		document.Management.HeaderTimeout = *value
	}
	if value := patch.Management.RequestTimeout; value != nil {
		document.Management.RequestTimeout = *value
	}
	if value := patch.ServiceControl.Listen; value != nil {
		document.ServiceControl.Listen = *value
	}
	if value := patch.ServiceControl.PublicURL; value != nil {
		document.ServiceControl.PublicURL = *value
	}
	merged, err := json.Marshal(document)
	if err != nil {
		return nil, domain.CoreContractError{Operation: "settings", Message: "settings patch could not be encoded"}
	}
	return merged, nil
}

func coreSettingsErrorCode(err error) int {
	var contract domain.CoreContractError
	if errors.As(err, &contract) {
		return application.ExitValidation
	}
	var httpError domain.CoreHTTPError
	if errors.As(err, &httpError) {
		return httpCode(httpError.Status)
	}
	return application.ExitTarget
}

func projectCoreSettings(target domain.Target, snapshot domain.CoreSettingsSnapshot) (map[string]any, error) {
	if !safeTargetReferenceID.MatchString(target.Name) || len(snapshot.Settings) == 0 {
		return nil, errors.New("core settings view cannot be safely scoped to this target")
	}
	var settings coreSettingsDocument
	decoder := json.NewDecoder(strings.NewReader(string(snapshot.Settings)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil || decoder.Decode(new(any)) != io.EOF || settings.Management.Listen == "" || settings.ServiceControl.Listen == "" {
		return nil, domain.CoreContractError{Operation: "settings", Message: "unsupported settings document"}
	}
	secretRoot, _ := serviceSecretRoot(target)
	view := map[string]any{
		"schemaVersion": "core-settings-view/v1", "target": target.Name,
		"desiredRevision": snapshot.DesiredRevision, "effectiveRevision": snapshot.EffectiveRevision,
		"pending": snapshot.Pending, "restartRequired": snapshot.RestartRequired,
		"etag": snapshot.ETag,
		"settings": map[string]any{
			"management": map[string]any{
				"listen": settings.Management.Listen, "maxBodyBytes": settings.Management.MaxBodyBytes,
				"headerTimeout": settings.Management.HeaderTimeout, "requestTimeout": settings.Management.RequestTimeout,
				"tls": map[string]any{
					"certificate": corePathReference(target, settings.Management.TLS.Certificate, "/liapoldus/tls/management.crt", target.Bootstrap.ManagementCertificate, "management.tls.certificate"),
					"key":         corePathReference(target, settings.Management.TLS.Key, "/liapoldus/tls/management.key", target.Bootstrap.ManagementKey, "management.tls.key"),
					"clientCA":    corePathReference(target, settings.Management.TLS.ClientCA, "/liapoldus/tls/management-client-ca.pem", target.Bootstrap.ManagementClientCA, "management.tls.clientCA"),
				},
			},
			"serviceControl": map[string]any{
				"listen": settings.ServiceControl.Listen, "publicURL": settings.ServiceControl.PublicURL,
				"tls": map[string]any{
					"certificate": corePathReference(target, settings.ServiceControl.TLS.Certificate, "/liapoldus/tls/control.crt", target.Bootstrap.ControlCertificate, "serviceControl.tls.certificate"),
					"key":         corePathReference(target, settings.ServiceControl.TLS.Key, "/liapoldus/tls/control.key", target.Bootstrap.ControlKey, "serviceControl.tls.key"),
					"clientCA":    corePathReference(target, settings.ServiceControl.TLS.ClientCA, "", "", "serviceControl.tls.clientCA"),
				},
				"replicaClientCA":       corePathReference(target, settings.ServiceControl.ReplicaClientCA, "/liapoldus/tls/replica-client-ca.pem", target.Bootstrap.ReplicaClientCA, "serviceControl.replicaClientCA"),
				"replicaServerCA":       corePathReference(target, settings.ServiceControl.ReplicaServerCA, "/liapoldus/tls/replica-server-ca.pem", target.Bootstrap.ReplicaServerCA, "serviceControl.replicaServerCA"),
				"replicaClientCRLCount": len(settings.ServiceControl.ReplicaClientCRLs),
				"replicaServerCRLCount": len(settings.ServiceControl.ReplicaServerCRLs),
			},
			"secretRoot": corePathReference(target, settings.SecretRoot, "/var/lib/liapoldus/secrets", secretRoot, "secretRoot"),
		},
	}
	if settings.TrafficController != nil {
		controller := settings.TrafficController
		view["settings"].(map[string]any)["trafficController"] = map[string]any{
			"schemaVersion": controller.SchemaVersion, "listen": controller.Listen,
			"certificate":    corePathReference(target, controller.Certificate, "", "", "trafficController.certificate"),
			"key":            corePathReference(target, controller.Key, "", "", "trafficController.key"),
			"clientCA":       corePathReference(target, controller.ClientCA, "", "", "trafficController.clientCA"),
			"clientCRLCount": len(controller.ClientCRLs), "allowedIdentityCount": len(controller.AllowedIdentities),
		}
	}
	return view, nil
}

func corePathReference(target domain.Target, configuredPath, containerPath, sourcePath, field string) coreFileReferenceView {
	if configuredPath == "" {
		return coreFileReferenceView{State: "not_configured"}
	}
	if containerPath == "" || configuredPath != containerPath || sourcePath == "" {
		return coreFileReferenceView{Configured: true, State: "unmapped"}
	}
	if !safeTargetReferenceID.MatchString(target.Name) {
		return coreFileReferenceView{Configured: true, State: "unmapped"}
	}
	info, err := os.Stat(sourcePath)
	available := err == nil && info.Mode().IsRegular()
	referenceID := fmt.Sprintf("target-file/v1/%s/%s", target.Name, strings.TrimPrefix(containerPath, "/liapoldus/"))
	if strings.HasPrefix(containerPath, "/var/lib/liapoldus/") {
		referenceID = fmt.Sprintf("target-file/v1/%s/state/%s", target.Name, field)
	}
	return coreFileReferenceView{ReferenceID: referenceID, Configured: true, Available: available, State: "mapped"}
}
