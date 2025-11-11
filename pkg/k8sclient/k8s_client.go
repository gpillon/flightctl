package k8sclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultBurst         = 1000
	defaultQPS           = 500
	externalApiTokenPath = "/var/flightctl/k8s/token" //nolint:gosec
)

type K8SClient interface {
	GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error)
	CreateSecret(ctx context.Context, namespace string, secret *corev1.Secret) (*corev1.Secret, error)
	DeleteSecret(ctx context.Context, namespace, name string) error
	PostCRD(ctx context.Context, crdGVK string, body []byte, opts ...Option) ([]byte, error)
	CreateJob(ctx context.Context, namespace string, job *batchv1.Job) (*batchv1.Job, error)
	GetJob(ctx context.Context, namespace, name string) (*batchv1.Job, error)
	ListJobs(ctx context.Context, namespace string, labelSelector string) (*batchv1.JobList, error)
	DeleteJob(ctx context.Context, namespace, name string) error
	WatchJob(ctx context.Context, namespace, name string) error
	CreatePVC(ctx context.Context, namespace string, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error)
	DeletePVC(ctx context.Context, namespace, name string) error
	CreateConfigMap(ctx context.Context, namespace string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)
	GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error)
	DeleteConfigMap(ctx context.Context, namespace, name string) error
	ListPods(ctx context.Context, namespace string, labelSelector string) (*corev1.PodList, error)
	GetPodLogs(ctx context.Context, namespace, podName string, tailLines int64) (string, error)
}

type k8sClient struct {
	clientset *kubernetes.Clientset
}

func NewK8SClient() (K8SClient, error) {
	var config *rest.Config
	var err error

	// Try in-cluster config first (when running inside a pod)
	config, err = rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig (for local development)
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home := os.Getenv("HOME")
			if home == "" {
				home = os.Getenv("USERPROFILE") // Windows
			}
			kubeconfig = filepath.Join(home, ".kube", "config")
		}

		// Check if kubeconfig file exists
		if _, err := os.Stat(kubeconfig); os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to create in-cluster config and kubeconfig file not found at %s: %w", kubeconfig, err)
		}

		// Use the kubeconfig file
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create config from kubeconfig %s: %w", kubeconfig, err)
		}
	}

	config.Burst = defaultBurst
	config.QPS = defaultQPS
	return newClient(config)
}

func NewK8SExternalClient(apiUrl string, insecure bool, caCert string) (K8SClient, error) {
	config := &rest.Config{
		Host: apiUrl,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: insecure,
			CAData:   []byte(caCert),
		},
		Burst:           defaultBurst,
		QPS:             defaultQPS,
		BearerTokenFile: externalApiTokenPath,
	}

	return newClient(config)
}

func newClient(config *rest.Config) (K8SClient, error) {
	// Create a clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	return &k8sClient{clientset: clientset}, nil
}

func (k *k8sClient) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	secret, err := k.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}
	return secret, nil
}

func (k *k8sClient) CreateSecret(ctx context.Context, namespace string, secret *corev1.Secret) (*corev1.Secret, error) {
	createdSecret, err := k.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create secret %s/%s: %w", namespace, secret.Name, err)
	}
	return createdSecret, nil
}

func (k *k8sClient) DeleteSecret(ctx context.Context, namespace, name string) error {
	err := k.clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (k *k8sClient) PostCRD(ctx context.Context, crdGVK string, body []byte, opts ...Option) ([]byte, error) {
	req := k.clientset.RESTClient().Post().AbsPath(fmt.Sprintf("/apis/%s", crdGVK)).Body(body)
	for _, opt := range opts {
		opt(req)
	}
	return req.DoRaw(ctx)
}

type Option func(*rest.Request)

func WithToken(token string) Option {
	return func(req *rest.Request) {
		req.SetHeader("Authorization", fmt.Sprintf("Bearer %s", token))
	}
}

func (k *k8sClient) CreateJob(ctx context.Context, namespace string, job *batchv1.Job) (*batchv1.Job, error) {
	createdJob, err := k.clientset.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create job %s/%s: %w", namespace, job.Name, err)
	}
	return createdJob, nil
}

func (k *k8sClient) GetJob(ctx context.Context, namespace, name string) (*batchv1.Job, error) {
	job, err := k.clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get job %s/%s: %w", namespace, name, err)
	}
	return job, nil
}

func (k *k8sClient) ListJobs(ctx context.Context, namespace string, labelSelector string) (*batchv1.JobList, error) {
	jobs, err := k.clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs in %s with selector %s: %w", namespace, labelSelector, err)
	}
	return jobs, nil
}

func (k *k8sClient) DeleteJob(ctx context.Context, namespace, name string) error {
	propagationPolicy := metav1.DeletePropagationBackground
	err := k.clientset.BatchV1().Jobs(namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagationPolicy,
	})
	if err != nil {
		return fmt.Errorf("failed to delete job %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (k *k8sClient) WatchJob(ctx context.Context, namespace, name string) error {
	watcher, err := k.clientset.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", name),
	})
	if err != nil {
		return fmt.Errorf("failed to watch job %s/%s: %w", namespace, name, err)
	}
	defer watcher.Stop()

	timeout := time.After(30 * time.Minute) // 30 minute timeout for job completion
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for job %s/%s to complete", namespace, name)
		case event := <-watcher.ResultChan():
			if event.Type == watch.Error {
				return fmt.Errorf("watch error for job %s/%s", namespace, name)
			}
			job, ok := event.Object.(*batchv1.Job)
			if !ok {
				continue
			}
			// Check if job has completed successfully
			if job.Status.Succeeded > 0 {
				return nil
			}
			// Check if job has failed
			if job.Status.Failed > 0 {
				return fmt.Errorf("job %s/%s failed", namespace, name)
			}
		}
	}
}

func (k *k8sClient) CreatePVC(ctx context.Context, namespace string, pvc *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
	createdPVC, err := k.clientset.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create PVC %s/%s: %w", namespace, pvc.Name, err)
	}
	return createdPVC, nil
}

func (k *k8sClient) DeletePVC(ctx context.Context, namespace, name string) error {
	err := k.clientset.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete PVC %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (k *k8sClient) CreateConfigMap(ctx context.Context, namespace string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	createdConfigMap, err := k.clientset.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create ConfigMap %s/%s: %w", namespace, configMap.Name, err)
	}
	return createdConfigMap, nil
}

func (k *k8sClient) GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	configMap, err := k.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, name, err)
	}
	return configMap, nil
}

func (k *k8sClient) DeleteConfigMap(ctx context.Context, namespace, name string) error {
	err := k.clientset.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete ConfigMap %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (k *k8sClient) ListPods(ctx context.Context, namespace string, labelSelector string) (*corev1.PodList, error) {
	pods, err := k.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in %s with selector %s: %w", namespace, labelSelector, err)
	}
	return pods, nil
}

func (k *k8sClient) GetPodLogs(ctx context.Context, namespace, podName string, tailLines int64) (string, error) {
	req := k.clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		TailLines: &tailLines,
	})

	logs, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get logs for pod %s/%s: %w", namespace, podName, err)
	}
	defer logs.Close()

	// Read all logs
	buf := make([]byte, 0, 1024*100) // 100KB initial buffer
	tmp := make([]byte, 1024)
	for {
		n, err := logs.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}

	return string(buf), nil
}
