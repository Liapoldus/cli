package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/artifacts"
	"github.com/Liapoldus/cli/internal/infrastructure/git"
)

func TestRuntimePlanResolvesOnlyExactProjectArtifact(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/server/service.yaml", `id: server
version: 1.1.0
artifact:
  id: server
  version: 1.1.0
  platform: linux/arm64
  digest: DIGEST
runtime:
  command: server
  controlEndpoint: https://server:9443
  peerEndpoint: https://server:9643
`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")

	registry := artifacts.Registry{Path: filepath.Join(t.TempDir(), "artifacts.json")}
	for _, version := range []string{"1.1.0", "2.0.0"} {
		binary := filepath.Join(t.TempDir(), "server-"+version)
		if err := os.WriteFile(binary, []byte("binary-"+version), 0o700); err != nil {
			t.Fatal(err)
		}
		digest, err := digestFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Register(domain.Artifact{ID: "server", Version: version, Platform: "linux/arm64", Digest: digest, Path: binary}); err != nil {
			t.Fatal(err)
		}
		if version == "1.1.0" {
			manifestPath := filepath.Join(project, "services", "server", "service.yaml")
			body, readErr := os.ReadFile(manifestPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			updated := strings.Replace(string(body), "DIGEST", digest, 1)
			if err := os.WriteFile(manifestPath, []byte(updated), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "pin artifact")

	plan, err := (Runner{Source: git.Repository{}, Artifacts: registry}).planRuntime(context.Background(), commandOptions{project: project, revision: "HEAD", target: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 1 || plan.Services[0].Artifact.Version != "1.1.0" || plan.Services[0].Artifact.Trusted || plan.Services[0].Command != "server" || plan.Services[0].ControlEndpoint != "https://server:9443" {
		t.Fatalf("runtime plan did not preserve the exact Project binding: %+v", plan)
	}
}

func TestRuntimeStartRequiresExplicitArtifactTrust(t *testing.T) {
	plan := domain.RuntimePlan{Services: []domain.RuntimeServiceArtifact{{Artifact: domain.Artifact{ID: "echo", Version: "1.1.0", Platform: "linux/arm64"}}}}
	if err := validateRuntimePlanTrusted(plan); !errors.Is(err, domain.ErrArtifactUntrusted) {
		t.Fatalf("untrusted artifact error = %v", err)
	}
	plan.Services[0].Artifact.Trusted = true
	if err := validateRuntimePlanTrusted(plan); err != nil {
		t.Fatalf("trusted artifact was rejected: %v", err)
	}
}

func TestRuntimeContainerAssignsDistinctPlacementPerReplica(t *testing.T) {
	root := t.TempDir()
	caFile := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caFile, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	tlsDirectory := filepath.Join(root, "tls")
	if err := os.MkdirAll(tlsDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"server", "echo"} {
		identity := "spiffe://liapoldus/plugin/" + id + "/" + id + "/" + id + "-incarnation"
		writeRuntimeIdentityPair(t, tlsDirectory, id+".crt", id+".key", identity)
		writeRuntimeIdentityPair(t, tlsDirectory, id+"-peer.crt", id+"-peer.key", identity)
		if err := os.WriteFile(filepath.Join(tlsDirectory, "revoked.pem"), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := domain.Target{
		CAFile: caFile, RuntimeImage: "alpine:latest", RuntimePlatform: "linux/arm64",
		Bootstrap: domain.CoreBootstrap{ControlListen: "0.0.0.0:18181"},
		Runtime:   domain.RuntimeConfig{TLSDirectory: tlsDirectory, CoreServerName: "localhost", PeerPort: 9643},
	}
	for _, id := range []string{"server", "echo"} {
		request, err := runtimeContainerRequest(target, domain.RuntimeServiceArtifact{
			Artifact: domain.Artifact{ID: id}, Command: id,
			ControlEndpoint: "https://" + id + ":9443", PeerEndpoint: "https://" + id + ":9643",
		}, filepath.Join(root, id), "runtime-network", "runtime-label")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := request.Environment["LIAPOLDUS_PLACEMENT_ID"], "container-"+id; got != want {
			t.Fatalf("%s placement ID = %q, want %q", id, got, want)
		}
		if got, want := request.Environment["LIAPOLDUS_PEER_IDENTITY"], "spiffe://liapoldus/plugin/"+id+"/"+id+"/"+id+"-incarnation"; got != want {
			t.Fatalf("%s peer identity = %q, want %q", id, got, want)
		}
	}
}

func TestRuntimeContainerSeparatesProductAndSDKPublicListeners(t *testing.T) {
	root := t.TempDir()
	caFile := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caFile, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	tlsDirectory := filepath.Join(root, "tls")
	if err := os.MkdirAll(tlsDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := "spiffe://liapoldus/plugin/server/server/server-incarnation"
	writeRuntimeIdentityPair(t, tlsDirectory, "server.crt", "server.key", identity)
	writeRuntimeIdentityPair(t, tlsDirectory, "server-peer.crt", "server-peer.key", identity)
	if err := os.WriteFile(filepath.Join(tlsDirectory, "revoked.pem"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := domain.Target{
		CAFile: caFile, RuntimeImage: "alpine:latest", RuntimePlatform: "linux/arm64",
		Bootstrap: domain.CoreBootstrap{ControlListen: "0.0.0.0:18181"},
		Runtime: domain.RuntimeConfig{
			TLSDirectory: tlsDirectory, CoreServerName: "localhost", PeerPort: 9643,
			PublicPort: 19080, PublicContainerPort: 9080,
			PublicAPIHostPort: 19081, PublicAPIContainerPort: 9081, PublicServiceID: "server",
		},
	}
	request, err := runtimeContainerRequest(target, domain.RuntimeServiceArtifact{
		Artifact: domain.Artifact{ID: "server"}, Command: "server",
		ControlEndpoint: "https://server:9443", PeerEndpoint: "https://server:9643",
	}, filepath.Join(root, "server"), "runtime-network", "runtime-label")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := request.Environment["LIAPOLDUS_PUBLIC_LISTEN"], "0.0.0.0:9081"; got != want {
		t.Fatalf("SDK public listener = %q, want %q", got, want)
	}
	if len(request.PublishedPorts) != 2 || request.PublishedPorts[0].Host != 19080 || request.PublishedPorts[0].Container != 9080 || request.PublishedPorts[1].Host != 19081 || request.PublishedPorts[1].Container != 9081 {
		t.Fatalf("product and SDK ports were not published separately: %+v", request.PublishedPorts)
	}
}

func TestRuntimePublicPortsRejectListenerCollision(t *testing.T) {
	if _, err := runtimePublicPorts(domain.RuntimeConfig{PublicPort: 19080, PublicContainerPort: 9080, PublicAPIHostPort: 19081, PublicAPIContainerPort: 9080}); err == nil {
		t.Fatal("expected colliding product and SDK container ports to be rejected")
	}
}

func TestRuntimePeerIdentityRejectsCertificateThatCannotMatchCoreDirectory(t *testing.T) {
	directory := t.TempDir()
	writeRuntimeIdentityPair(t, directory, "server.crt", "server.key", "spiffe://liapoldus/plugin/server/server/server-incarnation")
	writeRuntimeIdentityPair(t, directory, "server-peer.crt", "server-peer.key", "spiffe://liapoldus/plugin/server")
	if _, err := runtimePeerIdentity(filepath.Join(directory, "server.crt"), filepath.Join(directory, "server.key"), filepath.Join(directory, "server-peer.crt"), filepath.Join(directory, "server-peer.key"), "server"); err == nil || !strings.Contains(err.Error(), "must match the Core replica certificate identity") {
		t.Fatalf("mismatched protocol identity error = %v", err)
	}
}

func writeRuntimeIdentityPair(t *testing.T, directory, certificateName, keyName, identity string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(identity)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: certificateName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{uri},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})
	if err := os.WriteFile(filepath.Join(directory, certificateName), certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, keyName), privateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePlanRejectsArtifactDigestMismatch(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/server/service.yaml", `id: server
version: 1.1.0
artifact:
  id: server
  version: 1.1.0
  platform: linux/arm64
  digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
runtime:
  command: server
  controlEndpoint: https://server:9443
  peerEndpoint: https://server:9643
`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")

	binary := filepath.Join(t.TempDir(), "server")
	if err := os.WriteFile(binary, []byte("trusted-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := digestFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	registry := artifacts.Registry{Path: filepath.Join(t.TempDir(), "artifacts.json")}
	if err := registry.Register(domain.Artifact{ID: "server", Version: "1.1.0", Platform: "linux/arm64", Digest: digest, Path: binary}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Trust("server", "1.1.0", "linux/arm64", digest); err != nil {
		t.Fatal(err)
	}
	_, err = (Runner{Source: git.Repository{}, Artifacts: registry}).planRuntime(context.Background(), commandOptions{project: project, revision: "HEAD", target: "local"})
	if !errors.Is(err, domain.ErrArtifactDigestMismatch) {
		t.Fatalf("digest mismatch error = %v", err)
	}
}
