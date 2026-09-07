// Package kubernetes runs each step of a workflow as a POD of its own.
//
// The dynamic is section 2's, and it is what separates this engine from a
// monolithic worker: the pod starts with the step's EXACT image, runs a command,
// reports and dies. A dbt step starts the dbt image with 1Gi; the Go fetcher next
// to it starts a 10 MB image with 32Mi. Under a single image both would pay the
// larger of the two — in pull bytes, in reserved memory and in surface.
//
// The client is written on the stdlib, with no client-go. The official library
// brings hundreds of dependencies and tens of MB for what here are four calls
// REST: create a pod, read its status, read its log, delete it. The same choice
// was already made
// for React (a vendored bundle) and for the CSS (standalone Tailwind): a large
// dependency's cost only pays for itself when a large fraction of it is used.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// The paths the kubelet mounts in every pod with a service account.
const (
	dirSA     = "/var/run/secrets/kubernetes.io/serviceaccount"
	tokenFile = dirSA + "/token"
	caFile    = dirSA + "/ca.crt"
	nsFile    = dirSA + "/namespace"
)

// Cliente speaks to the API server.
type Cliente struct {
	base      string
	namespace string
	http      *http.Client

	// The token is read off disk on every use, with a short cache. Projected
	// tokens EXPIRE and the kubelet rewrites them in place; keeping the value
	// from boot makes the process start getting 401s after an hour — a failure
	// that shows up
	// tarde e parece problema de RBAC.
	mu        sync.Mutex
	token     string
	tokenLido time.Time
	tokenTTL  time.Duration
	readFile  func(string) ([]byte, error)
}

// ErrOutsideCluster is returned when there is no service account mounted.
type ErrOutsideCluster struct{ Reason string }

func (e ErrOutsideCluster) Error() string {
	return "fora de um cluster Kubernetes: " + e.Reason
}

// NoCluster builds the client out of the environment the kubelet injects.
func NoCluster() (*Cliente, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, ErrOutsideCluster{Reason: "KUBERNETES_SERVICE_HOST/PORT ausentes"}
	}
	ns, err := os.ReadFile(nsFile)
	if err != nil {
		return nil, ErrOutsideCluster{Reason: "namespace not mounted: " + err.Error()}
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, ErrOutsideCluster{Reason: "CA not mounted: " + err.Error()}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("CA do cluster invalida")
	}

	transporte := http.DefaultTransport.(*http.Transport).Clone()
	transporte.TLSClientConfig = tlsConfig{pool}.build()

	return &Cliente{
		base:      fmt.Sprintf("https://%s", net_(host, port)),
		namespace: strings.TrimSpace(string(ns)),
		// No timeout on the client: the log GET with follow stays open for the
		// task's whole duration. The cut comes from each call's context.
		http:     &http.Client{Transport: transporte},
		tokenTTL: time.Minute,
		readFile: os.ReadFile,
	}, nil
}

// Namespace is where the pods are created.
func (c *Cliente) Namespace() string { return c.namespace }

func (c *Cliente) authorize(r *http.Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || time.Since(c.tokenLido) > c.tokenTTL {
		b, err := c.readFile(tokenFile)
		if err != nil {
			return fmt.Errorf("lendo token da service account: %w", err)
		}
		c.token, c.tokenLido = strings.TrimSpace(string(b)), time.Now()
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	return nil
}

func (c *Cliente) request(ctx context.Context, metodo, path string, corpo any) (*http.Response, error) {
	var leitor io.Reader
	if corpo != nil {
		b, err := json.Marshal(corpo)
		if err != nil {
			return nil, err
		}
		leitor = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, metodo, c.base+path, leitor)
	if err != nil {
		return nil, err
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.authorize(req); err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// apiError turns Kubernetes's Status into a readable error. The body carries
// the real reason ("pods is forbidden: ... cannot create resource"), and
// discarding it would leave only "422", which helps nobody.
func apiError(res *http.Response) error {
	defer func() { _ = res.Body.Close() }()
	var status struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	corpo, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	_ = json.Unmarshal(corpo, &status)
	if status.Message != "" {
		return fmt.Errorf("kubernetes %s: %s", res.Status, status.Message)
	}
	return fmt.Errorf("kubernetes %s: %s", res.Status, strings.TrimSpace(string(corpo)))
}

// CreatePod creates the pod and returns the name it was given.
func (c *Cliente) CreatePod(ctx context.Context, p Pod) (Pod, error) {
	res, err := c.request(ctx, http.MethodPost,
		"/api/v1/namespaces/"+c.namespace+"/pods", p)
	if err != nil {
		return Pod{}, err
	}
	if res.StatusCode >= 300 {
		return Pod{}, apiError(res)
	}
	defer func() { _ = res.Body.Close() }()

	var created Pod
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		return Pod{}, fmt.Errorf("lendo pod criado: %w", err)
	}
	return created, nil
}

// LerPod devolve o estado atual.
func (c *Cliente) LerPod(ctx context.Context, name string) (Pod, error) {
	res, err := c.request(ctx, http.MethodGet,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+name, nil)
	if err != nil {
		return Pod{}, err
	}
	if res.StatusCode >= 300 {
		return Pod{}, apiError(res)
	}
	defer func() { _ = res.Body.Close() }()

	var p Pod
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		return Pod{}, err
	}
	return p, nil
}

// Logs opens the container's output stream. With `follow`, the response only
// ends when the container ends — which is why there is no timeout on the
// http.Client.
func (c *Cliente) Logs(ctx context.Context, name string, follow1 bool) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("container", containerName)
	if follow1 {
		q.Set("follow", "true")
	}
	res, err := c.request(ctx, http.MethodGet,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+name+"/log?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, apiError(res)
	}
	return res.Body, nil
}

// DeletePod remove o pod.
func (c *Cliente) DeletePod(ctx context.Context, name string) error {
	res, err := c.request(ctx, http.MethodDelete,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+name, nil)
	if err != nil {
		return err
	}
	// A 404 is success for a delete: the goal was for the pod to be gone.
	if res.StatusCode >= 300 && res.StatusCode != http.StatusNotFound {
		return apiError(res)
	}
	_ = res.Body.Close()
	return nil
}
