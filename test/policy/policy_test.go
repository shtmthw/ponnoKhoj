// Package policy renders the Kustomize overlays and checks the rules every
// ponnoKhoj workload must follow: resource requests and limits, health
// probes, a hardened security context, and the environment-specific
// guarantees (prod has replicas, autoscaling, three database instances and
// no committed secrets). kubeconform checks the schemas; this checks policy.
package policy

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

const deployDir = "../../deploy/k8s"

// object is a rendered Kubernetes object as generic data.
type object map[string]any

func (o object) kind() string { return str(o, "kind") }
func (o object) name() string { return str(o, "metadata", "name") }

// get walks nested maps; it returns nil when any step is missing.
func get(v any, path ...string) any {
	if o, ok := v.(object); ok {
		v = map[string]any(o)
	}
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func str(v any, path ...string) string {
	s, _ := get(v, path...).(string)
	return s
}

func list(v any, path ...string) []any {
	l, _ := get(v, path...).([]any)
	return l
}

// render builds an overlay from an in-memory copy of deploy/k8s, so the test
// never writes into the repository. The dev overlay's git-ignored secrets
// file is replaced by placeholder values.
func render(t *testing.T, overlay string) []object {
	t.Helper()
	mem := filesys.MakeFsInMemory()
	root, err := filepath.Abs(deployDir)
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == ".env.dev" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return mem.WriteFile(path, data)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.WriteFile(filepath.Join(root, "overlays/dev/.env.dev"), []byte("APP_SECRET=placeholder-placeholder-placeholder-00\nSEED_ADMIN_PASSWORD=placeholder\nSEED_VENDOR_PASSWORD=placeholder\n")); err != nil {
		t.Fatal(err)
	}
	resources, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(mem, filepath.Join(root, "overlays", overlay))
	if err != nil {
		t.Fatalf("kustomize build %s: %v", overlay, err)
	}
	out, err := resources.AsYaml()
	if err != nil {
		t.Fatal(err)
	}
	var objs []object
	for _, doc := range strings.Split(string(out), "\n---\n") {
		var o object
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("parse rendered %s: %v", overlay, err)
		}
		if len(o) > 0 {
			objs = append(objs, o)
		}
	}
	return objs
}

func find(objs []object, kind, name string) object {
	for _, o := range objs {
		if o.kind() == kind && (name == "" || o.name() == name) {
			return o
		}
	}
	return nil
}

func configValue(t *testing.T, objs []object, key string) string {
	t.Helper()
	for _, o := range objs {
		if o.kind() == "ConfigMap" && strings.HasPrefix(o.name(), "ponnokhoj-config") {
			return str(o, "data", key)
		}
	}
	t.Fatal("no ponnokhoj-config ConfigMap rendered")
	return ""
}

// checkPod applies the container rules to a pod template.
func checkPod(t *testing.T, where string, pod any, wantProbes bool) {
	t.Helper()
	spec := get(pod, "spec")
	if get(spec, "automountServiceAccountToken") != false {
		t.Errorf("%s: automountServiceAccountToken must be false", where)
	}
	if str(spec, "securityContext", "seccompProfile", "type") != "RuntimeDefault" {
		t.Errorf("%s: pod seccompProfile must be RuntimeDefault", where)
	}
	if get(spec, "securityContext", "runAsNonRoot") != true {
		t.Errorf("%s: pod must set runAsNonRoot", where)
	}
	containers := append(list(spec, "initContainers"), list(spec, "containers")...)
	if len(containers) == 0 {
		t.Errorf("%s: no containers", where)
	}
	for _, c := range containers {
		cw := where + "/" + str(c, "name")
		for _, r := range []string{"requests/cpu", "requests/memory", "limits/cpu", "limits/memory"} {
			parts := strings.Split(r, "/")
			if get(c, "resources", parts[0], parts[1]) == nil {
				t.Errorf("%s: missing resources.%s.%s", cw, parts[0], parts[1])
			}
		}
		sc := get(c, "securityContext")
		if get(sc, "allowPrivilegeEscalation") != false {
			t.Errorf("%s: allowPrivilegeEscalation must be false", cw)
		}
		if get(sc, "readOnlyRootFilesystem") != true {
			t.Errorf("%s: readOnlyRootFilesystem must be true", cw)
		}
		if get(sc, "runAsNonRoot") != true {
			t.Errorf("%s: runAsNonRoot must be true", cw)
		}
		if drop := list(sc, "capabilities", "drop"); len(drop) != 1 || drop[0] != "ALL" {
			t.Errorf("%s: capabilities must drop ALL, got %v", cw, drop)
		}
		if add := list(sc, "capabilities", "add"); len(add) > 0 {
			t.Errorf("%s: adds capabilities %v", cw, add)
		}
		if get(sc, "privileged") == true {
			t.Errorf("%s: privileged", cw)
		}
		if image := str(c, "image"); image == "" || strings.HasSuffix(image, ":latest") {
			t.Errorf("%s: image %q must be set and not :latest", cw, image)
		}
		if wantProbes {
			for _, p := range []string{"startupProbe", "livenessProbe", "readinessProbe"} {
				if get(c, p) == nil {
					t.Errorf("%s: missing %s", cw, p)
				}
			}
			// /livez must not depend on the database; /readyz must.
			if str(c, "livenessProbe", "httpGet", "path") != "/livez" || str(c, "readinessProbe", "httpGet", "path") != "/readyz" {
				t.Errorf("%s: liveness must probe /livez and readiness /readyz", cw)
			}
		}
	}
}

func checkCommon(t *testing.T, overlay string, objs []object) {
	t.Helper()
	ns := find(objs, "Namespace", "ponnokhoj")
	if str(ns, "metadata", "labels", "pod-security.kubernetes.io/enforce") != "restricted" {
		t.Errorf("%s: namespace must enforce the restricted Pod Security Standard", overlay)
	}
	if sa := find(objs, "ServiceAccount", "ponnokhoj"); sa == nil || get(sa, "automountServiceAccountToken") != false {
		t.Errorf("%s: ServiceAccount must disable token automounting", overlay)
	}
	drain, err1 := time.ParseDuration(configValue(t, objs, "SHUTDOWN_DRAIN_DELAY"))
	shutdown, err2 := time.ParseDuration(configValue(t, objs, "SHUTDOWN_TIMEOUT"))
	if err1 != nil || err2 != nil {
		t.Fatalf("%s: shutdown durations unparsable", overlay)
	}
	deployments := 0
	for _, o := range objs {
		switch o.kind() {
		case "Deployment":
			deployments++
			where := overlay + "/" + o.name()
			checkPod(t, where, get(o, "spec", "template"), true)
			if v, _ := get(o, "spec", "strategy", "rollingUpdate", "maxUnavailable").(float64); v != 0 || get(o, "spec", "strategy", "rollingUpdate", "maxUnavailable") == nil {
				t.Errorf("%s: rollingUpdate.maxUnavailable must be 0", where)
			}
			grace, _ := get(o, "spec", "template", "spec", "terminationGracePeriodSeconds").(float64)
			if time.Duration(grace)*time.Second <= drain+shutdown {
				t.Errorf("%s: terminationGracePeriodSeconds %vs must exceed drain %s + shutdown %s", where, grace, drain, shutdown)
			}
			if len(list(o, "spec", "template", "spec", "topologySpreadConstraints")) == 0 {
				t.Errorf("%s: missing topologySpreadConstraints", where)
			}
		case "Job":
			checkPod(t, overlay+"/"+o.name(), get(o, "spec", "template"), false)
		case "Ingress":
			t.Errorf("%s: uses the Ingress API; ingress is Gateway API only", overlay)
		case "Pod", "DaemonSet", "StatefulSet", "CronJob":
			t.Errorf("%s: unexpected workload kind %s", overlay, o.kind())
		}
	}
	if deployments != 2 {
		t.Errorf("%s: %d Deployments, want web and worker", overlay, deployments)
	}
	for _, name := range []string{"ponnokhoj-web", "ponnokhoj-worker"} {
		if find(objs, "PodDisruptionBudget", name) == nil {
			t.Errorf("%s: missing PodDisruptionBudget %s", overlay, name)
		}
	}
	deny := find(objs, "NetworkPolicy", "default-deny-all")
	if deny == nil || len(get(deny, "spec", "podSelector").(map[string]any)) != 0 || len(list(deny, "spec", "policyTypes")) != 2 {
		t.Errorf("%s: missing a default-deny NetworkPolicy for ingress and egress", overlay)
	}
	// The metrics port is never routed through the Gateway.
	for _, o := range objs {
		if o.kind() != "HTTPRoute" {
			continue
		}
		for _, rule := range list(o, "spec", "rules") {
			for _, ref := range list(rule, "backendRefs") {
				if str(ref, "name") == "ponnokhoj-metrics" || get(ref, "port") == float64(9090) {
					t.Errorf("%s: HTTPRoute %s exposes the metrics port", overlay, o.name())
				}
			}
		}
	}
	db := find(objs, "Cluster", "ponnokhoj-db")
	if db == nil || !strings.Contains(str(db, "spec", "imageName"), "@sha256:") {
		t.Errorf("%s: the CloudNativePG image must be pinned by digest", overlay)
	}
}

func TestDevOverlay(t *testing.T) {
	t.Parallel()
	objs := render(t, "dev")
	checkCommon(t, "dev", objs)
	if configValue(t, objs, "PONNOKHOJ_ENV") != "dev" {
		t.Error("dev overlay is not PONNOKHOJ_ENV=dev")
	}
	job := find(objs, "Job", "ponnokhoj-migrate")
	if args := list(job, "spec", "template", "spec", "containers")[0].(map[string]any)["args"]; strings.Join(toStrings(args), " ") != "seed --demo" {
		t.Errorf("dev seed args = %v, want seed --demo", args)
	}
	if find(objs, "Issuer", "ponnokhoj-issuer") == nil {
		t.Error("dev overlay has no certificate issuer")
	}
}

// The CI overlay is dev made offline: the demo shop instead of real stores
// and no LLM, so the e2e gate needs no secrets and no live network.
func TestCIOverlay(t *testing.T) {
	t.Parallel()
	objs := render(t, "ci")
	checkCommon(t, "ci", objs)
	if configValue(t, objs, "SOURCES_ENABLED") != "demoshop" || configValue(t, objs, "DEMO_SHOP_ENABLED") != "true" || configValue(t, objs, "LLM_ENABLED") != "false" {
		t.Error("ci overlay must use only the demo shop and no LLM")
	}
	if find(objs, "NetworkPolicy", "worker-egress-to-demo-shop") == nil {
		t.Error("ci overlay must let the worker reach the demo shop")
	}
}

func TestProdOverlay(t *testing.T) {
	t.Parallel()
	objs := render(t, "prod")
	checkCommon(t, "prod", objs)
	if configValue(t, objs, "PONNOKHOJ_ENV") != "prod" || configValue(t, objs, "COOKIE_SECURE") != "true" || configValue(t, objs, "DEMO_SHOP_ENABLED") != "false" {
		t.Error("prod config must be PONNOKHOJ_ENV=prod with secure cookies and no demo shop")
	}
	for _, name := range []string{"ponnokhoj-web", "ponnokhoj-worker"} {
		if r, _ := get(find(objs, "Deployment", name), "spec", "replicas").(float64); r < 2 {
			t.Errorf("prod %s replicas = %v, want >= 2", name, r)
		}
	}
	hpa := find(objs, "HorizontalPodAutoscaler", "ponnokhoj-web")
	if hpa == nil || get(hpa, "spec", "minReplicas").(float64) < 2 {
		t.Error("prod needs a CPU HPA for web with minReplicas >= 2")
	}
	db := find(objs, "Cluster", "ponnokhoj-db")
	if get(db, "spec", "instances") != float64(3) || len(list(db, "spec", "plugins")) == 0 {
		t.Error("prod database must have 3 instances and the backup plugin")
	}
	if find(objs, "ScheduledBackup", "") == nil || find(objs, "ObjectStore", "") == nil {
		t.Error("prod must schedule database backups to an object store")
	}
	// No secret material is rendered for prod: it comes from External Secrets.
	if s := find(objs, "Secret", ""); s != nil {
		t.Errorf("prod renders Secret %s; secrets must come from the external store", s.name())
	}
	if find(objs, "ExternalSecret", "ponnokhoj-secrets") == nil {
		t.Error("prod has no ExternalSecret for ponnokhoj-secrets")
	}
	// Demo data is never loaded in prod.
	job := find(objs, "Job", "ponnokhoj-migrate")
	for _, c := range list(job, "spec", "template", "spec", "containers") {
		if strings.Contains(strings.Join(toStrings(get(c, "args")), " "), "--demo") {
			t.Error("prod seeds demo data")
		}
	}
	// HTTP only redirects; the app is served on HTTPS.
	redirect := find(objs, "HTTPRoute", "ponnokhoj-https-redirect")
	rules := list(redirect, "spec", "rules")
	if redirect == nil || len(rules) == 0 || len(list(rules[0], "filters")) == 0 ||
		str(list(rules[0], "filters")[0], "type") != "RequestRedirect" ||
		str(list(rules[0], "filters")[0], "requestRedirect", "scheme") != "https" {
		t.Error("prod must redirect HTTP to HTTPS")
	}
	if ref := list(find(objs, "HTTPRoute", "ponnokhoj"), "spec", "parentRefs"); len(ref) != 1 || str(ref[0], "sectionName") != "https" {
		t.Error("prod application route must attach to the HTTPS listener only")
	}
	if str(find(objs, "Certificate", "ponnokhoj-tls"), "spec", "issuerRef", "kind") != "ClusterIssuer" {
		t.Error("prod certificate must come from a ClusterIssuer")
	}
	for _, o := range objs {
		if o.kind() == "Deployment" {
			image := str(list(o, "spec", "template", "spec", "containers")[0], "image")
			if !strings.HasPrefix(image, "docker.io/") {
				t.Errorf("prod %s image %q is not from Docker Hub", o.name(), image)
			}
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range toList(v) {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func toList(v any) []any {
	l, _ := v.([]any)
	return l
}
