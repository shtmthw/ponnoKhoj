// Command devctl is ponnoKhoj's local and CI deployment tool: it creates the
// kind cluster with its infrastructure (CloudNativePG, cert-manager, Envoy
// Gateway), loads images, and runs the phased deploy:
//
//  1. everything except the Deployments and the migration Job; wait for the
//     database to be Ready;
//  2. the migration Job, named after the image tag; wait for it to complete;
//  3. the Deployments; wait for the rollout and for the Gateway.
//
// Every kubectl call names its context explicitly, so devctl never acts on
// whatever cluster the developer's current kubectl context points at.
//
// Run it from the repository root (the Makefile does).
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	clusterName = "ponnokhoj"
	kindContext = "kind-" + clusterName
	namespace   = "ponnokhoj"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "kind-up":
		err = kindUp(os.Args[2:])
	case "kind-down":
		err = run("kind", "delete", "cluster", "--name", clusterName)
	case "kind-load":
		err = kindLoad(os.Args[2:])
	case "deploy":
		err = deploy(os.Args[2:])
	case "dev-secrets":
		err = devSecrets()
	case "compose-env":
		err = composeEnv()
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "devctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  devctl kind-up                       create the kind cluster and install its infrastructure
  devctl kind-down                     delete the kind cluster
  devctl kind-load -image IMAGE        load a local image into the kind cluster
  devctl deploy -overlay dev|ci|prod -image IMAGE [-context NAME]
  devctl dev-secrets                   create deploy/k8s/overlays/dev/.env.dev if missing
  devctl compose-env                   create deploy/compose/.env if missing
`)
	os.Exit(2)
}

// versions reads deploy/versions.env (KEY=value lines).
func versions() (map[string]string, error) {
	f, err := os.Open("deploy/versions.env")
	if err != nil {
		return nil, fmt.Errorf("run devctl from the repository root: %w", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("deploy/versions.env: malformed line %q", line)
		}
		out[k] = v
	}
	return out, sc.Err()
}

func run(name string, args ...string) error {
	fmt.Fprintf(os.Stderr, "+ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // developer tool running fixed commands
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func output(name string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // developer tool running fixed commands
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func kubectl(ctx string, args ...string) error {
	return run("kubectl", append([]string{"--context", ctx}, args...)...)
}

// retry runs fn until it succeeds or the timeout passes. Freshly installed
// CRDs and webhooks take a moment before the API server accepts objects.
func retry(what string, timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: %w", what, err)
		}
		fmt.Fprintf(os.Stderr, "  %s not ready yet, retrying...\n", what)
		time.Sleep(5 * time.Second)
	}
}

func kindUp(args []string) error {
	fs := flag.NewFlagSet("kind-up", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	v, err := versions()
	if err != nil {
		return err
	}
	clusters, err := output("kind", "get", "clusters")
	if err != nil {
		return fmt.Errorf("kind get clusters (is Docker running?): %w", err)
	}
	if !slices.Contains(strings.Fields(clusters), clusterName) {
		if err := run("kind", "create", "cluster", "--name", clusterName,
			"--image", v["KIND_NODE_IMAGE"], "--config", "deploy/kind/cluster.yaml", "--wait", "120s"); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(os.Stderr, "kind cluster", clusterName, "already exists; reconciling its infrastructure")
	}

	installs := []struct {
		name, url, ns string
	}{
		{"CloudNativePG " + v["CNPG_VERSION"], fmt.Sprintf("https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v%[1]s/cnpg-%[1]s.yaml", v["CNPG_VERSION"]), "cnpg-system"},
		{"cert-manager " + v["CERT_MANAGER_VERSION"], fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", v["CERT_MANAGER_VERSION"]), "cert-manager"},
		// Includes the Gateway API CRDs Envoy Gateway is tested with.
		{"Envoy Gateway " + v["ENVOY_GATEWAY_VERSION"], fmt.Sprintf("https://github.com/envoyproxy/gateway/releases/download/%s/install.yaml", v["ENVOY_GATEWAY_VERSION"]), "envoy-gateway-system"},
	}
	for _, in := range installs {
		fmt.Fprintln(os.Stderr, "== installing", in.name)
		if err := kubectl(kindContext, "apply", "--server-side", "--force-conflicts", "-f", in.url); err != nil {
			return fmt.Errorf("install %s: %w", in.name, err)
		}
		if err := kubectl(kindContext, "wait", "--namespace", in.ns, "--for=condition=Available", "deployment", "--all", "--timeout=300s"); err != nil {
			return fmt.Errorf("wait for %s: %w", in.name, err)
		}
	}
	fmt.Fprintln(os.Stderr, "== GatewayClass \"ponnokhoj\" (Envoy Gateway, NodePorts 30080/30443)")
	if err := retry("GatewayClass", 2*time.Minute, func() error {
		return kubectl(kindContext, "apply", "-f", "deploy/kind/gatewayclass.yaml")
	}); err != nil {
		return err
	}
	return devSecrets()
}

func kindLoad(args []string) error {
	fs := flag.NewFlagSet("kind-load", flag.ExitOnError)
	image := fs.String("image", "", "local image to load")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" {
		return errors.New("kind-load: -image is required")
	}
	return run("kind", "load", "docker-image", "--name", clusterName, *image)
}

// devSecrets creates the dev overlay's git-ignored secrets file with random
// values. It never overwrites an existing file.
func devSecrets() error {
	return writeSecretsEnv(filepath.Join("deploy", "k8s", "overlays", "dev", ".env.dev"), false)
}

// composeEnv writes the git-ignored secrets file for deploy/compose.
func composeEnv() error {
	return writeSecretsEnv(filepath.Join("deploy", "compose", ".env"), true)
}

// writeSecretsEnv creates a git-ignored env file with fresh random secrets
// and demo passwords. An existing file is left alone, so passwords stay
// stable across redeploys.
func writeSecretsEnv(path string, withDB bool) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	random := func(n int) (string, error) {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	admin, err := random(12)
	if err != nil {
		return err
	}
	vendor, err := random(12)
	if err != nil {
		return err
	}
	content := fmt.Sprintf("APP_SECRET=%s\nSEED_ADMIN_PASSWORD=%s\nSEED_VENDOR_PASSWORD=%s\n", hex.EncodeToString(secret), admin, vendor)
	if withDB {
		db, err := random(18)
		if err != nil {
			return err
		}
		content += "POSTGRES_PASSWORD=" + db + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "created", path, "(git-ignored; demo account passwords are in it)")
	return nil
}

var dnsLabel = regexp.MustCompile(`[^a-z0-9-]+`)

func deploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	overlay := fs.String("overlay", "dev", "overlay under deploy/k8s/overlays")
	image := fs.String("image", "", "image reference, e.g. ponnokhoj:dev-abc123 or docker.io/user/ponnokhoj@sha256:...")
	kctx := fs.String("context", "", "kubectl context (defaults to the kind cluster for the dev and ci overlays; required otherwise)")
	timeout := fs.Duration("timeout", 10*time.Minute, "timeout for each wait")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" {
		return errors.New("deploy: -image is required")
	}
	if *kctx == "" {
		if *overlay != "dev" && *overlay != "ci" {
			return errors.New("deploy: -context is required for overlays other than dev and ci")
		}
		*kctx = kindContext
	}
	name, tag := splitImage(*image)
	// Job templates are immutable: every release gets a Job of its own.
	jobSuffix := strings.Trim(dnsLabel.ReplaceAllString(strings.ToLower(strings.TrimPrefix(tag, "sha256:")), "-"), "-")
	if len(jobSuffix) > 40 {
		jobSuffix = jobSuffix[:40]
	}
	jobName := "ponnokhoj-migrate-" + jobSuffix

	dir := filepath.Join("bin", "deploy", *overlay)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	imageSpec := "    newTag: " + tag
	if strings.HasPrefix(tag, "sha256:") {
		imageSpec = "    digest: " + tag
	}
	kustomization := fmt.Sprintf(`# Generated by devctl deploy; do not edit.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../../deploy/k8s/overlays/%s
images:
  - name: ponnokhoj
    newName: %s
%s
  - name: %s
    newName: %s
%s
patches:
  - target:
      kind: Job
      name: ponnokhoj-migrate
    patch: |-
      - op: replace
        path: /metadata/name
        value: %s
`, *overlay, name, imageSpec, overlayImageName(*overlay), name, imageSpec, jobName)
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kustomization), 0o600); err != nil {
		return err
	}
	rendered, err := output("go", "tool", "-modfile=tools/go.mod", "kustomize", "build", dir)
	if err != nil {
		return fmt.Errorf("kustomize build: %w", err)
	}
	manifest := filepath.Join(dir, "rendered.yaml")
	if err := os.WriteFile(manifest, []byte(rendered+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "== deploying %s to %s (job %s)\n", *image, *kctx, jobName)
	waitFor := "--timeout=" + timeout.String()

	fmt.Fprintln(os.Stderr, "== phase 1: namespace, config, database, network policy, gateway")
	if err := retry("phase 1 apply", 2*time.Minute, func() error {
		return kubectl(*kctx, "apply", "-f", manifest, "-l", "app.kubernetes.io/component notin (web,worker,migrate)")
	}); err != nil {
		return err
	}
	if err := kubectl(*kctx, "wait", "-n", namespace, "--for=condition=Ready", "cluster.postgresql.cnpg.io/ponnokhoj-db", waitFor); err != nil {
		return fmt.Errorf("database did not become ready: %w", err)
	}

	fmt.Fprintln(os.Stderr, "== phase 2: migrations and reference data")
	if err := kubectl(*kctx, "apply", "-f", manifest, "-l", "app.kubernetes.io/component=migrate"); err != nil {
		return err
	}
	if err := waitJob(*kctx, jobName, *timeout); err != nil {
		_ = kubectl(*kctx, "logs", "-n", namespace, "job/"+jobName, "--all-containers", "--tail=50")
		return err
	}

	fmt.Fprintln(os.Stderr, "== phase 3: web and worker")
	if err := kubectl(*kctx, "apply", "-f", manifest); err != nil {
		return err
	}
	for _, d := range []string{"ponnokhoj-web", "ponnokhoj-worker"} {
		if err := kubectl(*kctx, "rollout", "status", "-n", namespace, "deployment/"+d, waitFor); err != nil {
			return err
		}
	}
	if err := kubectl(*kctx, "wait", "-n", namespace, "--for=condition=Programmed", "gateway/ponnokhoj", waitFor); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "== deployed")
	return nil
}

// overlayImageName is the image name the overlay itself sets (prod points the
// placeholder at Docker Hub), so devctl's override applies to it too.
func overlayImageName(overlay string) string {
	if overlay == "prod" {
		return "docker.io/matthew552/ponnokhoj"
	}
	return "ponnokhoj-unused"
}

// waitJob polls a Job until it succeeds or fails (kubectl wait cannot wait
// for "either").
func waitJob(kctx, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := output("kubectl", "--context", kctx, "get", "job", "-n", namespace, name, "-o",
			`jsonpath={.status.conditions[?(@.type=="Complete")].status}/{.status.conditions[?(@.type=="Failed")].status}`)
		if err == nil {
			complete, failed, _ := strings.Cut(status, "/")
			if complete == "True" {
				fmt.Fprintln(os.Stderr, "  job", name, "complete")
				return nil
			}
			if failed == "True" {
				return fmt.Errorf("job %s failed", name)
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("job %s did not complete within %s", name, timeout)
}

// splitImage splits "repo/name:tag" or "repo/name@sha256:..." into name and
// tag (or digest).
func splitImage(ref string) (string, string) {
	if name, digest, ok := strings.Cut(ref, "@"); ok {
		return name, digest
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}
