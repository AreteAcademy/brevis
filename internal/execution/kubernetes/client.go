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
// REST: criar pod, ler status, ler log, apagar pod. A mesma escolha ja foi feita
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
	dirSA        = "/var/run/secrets/kubernetes.io/serviceaccount"
	arquivoToken = dirSA + "/token"
	arquivoCA    = dirSA + "/ca.crt"
	arquivoNS    = dirSA + "/namespace"
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
	mu         sync.Mutex
	token      string
	tokenLido  time.Time
	tokenTTL   time.Duration
	lerArquivo func(string) ([]byte, error)
}

// ErrForaDoCluster is returned when there is no service account mounted.
type ErrForaDoCluster struct{ Motivo string }

func (e ErrForaDoCluster) Error() string {
	return "fora de um cluster Kubernetes: " + e.Motivo
}

// NoCluster builds the client out of the environment the kubelet injects.
func NoCluster() (*Cliente, error) {
	host, porta := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || porta == "" {
		return nil, ErrForaDoCluster{Motivo: "KUBERNETES_SERVICE_HOST/PORT ausentes"}
	}
	ns, err := os.ReadFile(arquivoNS)
	if err != nil {
		return nil, ErrForaDoCluster{Motivo: "namespace nao montado: " + err.Error()}
	}
	ca, err := os.ReadFile(arquivoCA)
	if err != nil {
		return nil, ErrForaDoCluster{Motivo: "CA nao montada: " + err.Error()}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("CA do cluster invalida")
	}

	transporte := http.DefaultTransport.(*http.Transport).Clone()
	transporte.TLSClientConfig = tlsConfig{pool}.build()

	return &Cliente{
		base:      fmt.Sprintf("https://%s", net_(host, porta)),
		namespace: strings.TrimSpace(string(ns)),
		// No timeout on the client: the log GET with follow stays open for the
		// task's whole duration. The cut comes from each call's context.
		http:       &http.Client{Transport: transporte},
		tokenTTL:   time.Minute,
		lerArquivo: os.ReadFile,
	}, nil
}

// Namespace is where the pods are created.
func (c *Cliente) Namespace() string { return c.namespace }

func (c *Cliente) autorizar(r *http.Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || time.Since(c.tokenLido) > c.tokenTTL {
		b, err := c.lerArquivo(arquivoToken)
		if err != nil {
			return fmt.Errorf("lendo token da service account: %w", err)
		}
		c.token, c.tokenLido = strings.TrimSpace(string(b)), time.Now()
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	return nil
}

func (c *Cliente) requisicao(ctx context.Context, metodo, caminho string, corpo any) (*http.Response, error) {
	var leitor io.Reader
	if corpo != nil {
		b, err := json.Marshal(corpo)
		if err != nil {
			return nil, err
		}
		leitor = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, metodo, c.base+caminho, leitor)
	if err != nil {
		return nil, err
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.autorizar(req); err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// erroDaAPI turns Kubernetes's Status into a readable error. The body carries
// the real reason ("pods is forbidden: ... cannot create resource"), and
// discarding it would leave only "422", which helps nobody.
func erroDaAPI(res *http.Response) error {
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

// CriarPod cria o pod e devolve o nome atribuido.
func (c *Cliente) CriarPod(ctx context.Context, p Pod) (Pod, error) {
	res, err := c.requisicao(ctx, http.MethodPost,
		"/api/v1/namespaces/"+c.namespace+"/pods", p)
	if err != nil {
		return Pod{}, err
	}
	if res.StatusCode >= 300 {
		return Pod{}, erroDaAPI(res)
	}
	defer func() { _ = res.Body.Close() }()

	var criado Pod
	if err := json.NewDecoder(res.Body).Decode(&criado); err != nil {
		return Pod{}, fmt.Errorf("lendo pod criado: %w", err)
	}
	return criado, nil
}

// LerPod devolve o estado atual.
func (c *Cliente) LerPod(ctx context.Context, nome string) (Pod, error) {
	res, err := c.requisicao(ctx, http.MethodGet,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+nome, nil)
	if err != nil {
		return Pod{}, err
	}
	if res.StatusCode >= 300 {
		return Pod{}, erroDaAPI(res)
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
func (c *Cliente) Logs(ctx context.Context, nome string, seguir bool) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("container", nomeContainer)
	if seguir {
		q.Set("follow", "true")
	}
	res, err := c.requisicao(ctx, http.MethodGet,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+nome+"/log?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, erroDaAPI(res)
	}
	return res.Body, nil
}

// ApagarPod remove o pod.
func (c *Cliente) ApagarPod(ctx context.Context, nome string) error {
	res, err := c.requisicao(ctx, http.MethodDelete,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+nome, nil)
	if err != nil {
		return err
	}
	// A 404 is success for a delete: the goal was for the pod to be gone.
	if res.StatusCode >= 300 && res.StatusCode != http.StatusNotFound {
		return erroDaAPI(res)
	}
	_ = res.Body.Close()
	return nil
}
