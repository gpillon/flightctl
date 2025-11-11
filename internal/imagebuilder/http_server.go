package imagebuilder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

// HTTPServer provides HTTP API endpoints for the imagebuilder service
type HTTPServer struct {
	k8sClient    k8sclient.K8SClient
	cfg          *config.Config
	orchestrator *Orchestrator
	log          logrus.FieldLogger
}

// NewHTTPServer creates a new HTTPServer
func NewHTTPServer(
	k8sClient k8sclient.K8SClient,
	cfg *config.Config,
	orchestrator *Orchestrator,
	log logrus.FieldLogger,
) *HTTPServer {
	return &HTTPServer{
		k8sClient:    k8sClient,
		cfg:          cfg,
		orchestrator: orchestrator,
		log:          log.WithField("component", "http-server"),
	}
}

// Start starts the HTTP API server
func (hs *HTTPServer) Start(ctx context.Context) *http.Server {
	router := chi.NewRouter()

	// Middleware
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.RequestID)

	// Health check
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// API endpoints
	router.Get("/api/v1/imagebuilds/{name}/logs", hs.handleGetLogs)
	router.Get("/api/v1/imagebuilds/{name}/downloads/{filename}", hs.handleDownload)
	router.Post("/api/v1/imagebuilds/generate-containerfile", hs.handleGenerateContainerfile)
	router.With(hs.uploadAuthMiddleware).Post("/api/v1/imagebuilds/upload", hs.handleUploadArtifact)

	addr := ":9090" // HTTP port for API
	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	go func() {
		hs.log.Infof("Starting HTTP API server on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			hs.log.Fatalf("HTTP server error: %v", err)
		}
	}()

	return srv
}

// handleGetLogs retrieves logs for an ImageBuild from Kubernetes job pods
func (hs *HTTPServer) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "ImageBuild name is required", http.StatusBadRequest)
		return
	}

	buildNamespace := hs.getBuildNamespace()
	var allLogs []string

	// Find pods for the container build job
	labelSelector := fmt.Sprintf("job-name=build-%s", name)
	pods, err := hs.k8sClient.ListPods(r.Context(), buildNamespace, labelSelector)
	if err != nil {
		hs.log.WithError(err).Warnf("Failed to list pods for build job %s", name)
	} else {
		for _, pod := range pods.Items {
			podLog, err := hs.k8sClient.GetPodLogs(r.Context(), buildNamespace, pod.Name, 1000)
			if err != nil {
				hs.log.WithError(err).Warnf("Failed to get logs for pod %s", pod.Name)
				continue
			}

			if len(podLog) > 0 {
				allLogs = append(allLogs, fmt.Sprintf("=== Logs from container build pod %s ===", pod.Name))
				lines := strings.Split(podLog, "\n")
				allLogs = append(allLogs, lines...)
			}
		}
	}

	// Find pods for bootc image generation jobs
	allPods, err := hs.k8sClient.ListPods(r.Context(), buildNamespace, "")
	if err != nil {
		hs.log.WithError(err).Warnf("Failed to list all pods for bootc jobs %s", name)
	} else {
		bootcJobPrefix := fmt.Sprintf("bootc-%s-", name)
		for _, pod := range allPods.Items {
			if strings.HasPrefix(pod.Name, bootcJobPrefix) {
				podLog, err := hs.k8sClient.GetPodLogs(r.Context(), buildNamespace, pod.Name, 1000)
				if err != nil {
					hs.log.WithError(err).Warnf("Failed to get logs for bootc pod %s", pod.Name)
					continue
				}

				if len(podLog) > 0 {
					allLogs = append(allLogs, fmt.Sprintf("=== Logs from bootc image generation pod %s ===", pod.Name))
					lines := strings.Split(podLog, "\n")
					allLogs = append(allLogs, lines...)
				}
			}
		}
	}

	if len(allLogs) == 0 {
		allLogs = []string{"No logs available from build job pods"}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"logs": allLogs,
	})
}

// handleGenerateContainerfile generates a Containerfile from an ImageBuildSpec
func (hs *HTTPServer) handleGenerateContainerfile(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Spec           api.ImageBuildSpec `json:"spec"`
		EnrollmentCert string             `json:"enrollmentCert,omitempty"`
		EnrollmentKey  string             `json:"enrollmentKey,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		hs.log.WithError(err).Warn("Invalid request body for generate-containerfile")
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	generator := NewContainerfileGenerator(request.Spec, request.EnrollmentCert, request.EnrollmentKey)

	// Set default enrollment config
	if hs.cfg.Service != nil {
		caCertData := hs.orchestrator.GetCACertData()
		generator.WithDefaultEnrollmentConfig(
			caCertData,
			hs.cfg.Service.BaseAgentEndpointUrl,
			hs.cfg.Service.BaseUIUrl,
		)
	}

	containerfile, err := generator.Generate()
	if err != nil {
		hs.log.WithError(err).Error("Failed to generate Containerfile")
		http.Error(w, fmt.Sprintf("Failed to generate Containerfile: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"containerfile": containerfile,
	})
}

// handleUploadArtifact handles multipart upload of bootc image artifacts from build jobs
func (hs *HTTPServer) handleUploadArtifact(w http.ResponseWriter, r *http.Request) {
	hs.log.Info("Received upload request")

	// Use MultipartReader for streaming - avoids loading entire file in memory
	reader, err := r.MultipartReader()
	if err != nil {
		hs.log.WithError(err).Error("Failed to create multipart reader")
		http.Error(w, fmt.Sprintf("Failed to read multipart form: %v", err), http.StatusBadRequest)
		return
	}

	var imageName, imageType, architecture string
	var filePart *multipart.Part
	var filename string

	// Process multipart form parts as they stream in
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			hs.log.WithError(err).Error("Failed to read multipart part")
			http.Error(w, "Failed to read upload", http.StatusBadRequest)
			return
		}

		formName := part.FormName()

		// Handle metadata fields (small, can read into memory)
		if formName == "imageName" {
			buf := new(strings.Builder)
			io.Copy(buf, part)
			imageName = buf.String()
			part.Close()
		} else if formName == "imageType" {
			buf := new(strings.Builder)
			io.Copy(buf, part)
			imageType = buf.String()
			part.Close()
		} else if formName == "architecture" {
			buf := new(strings.Builder)
			io.Copy(buf, part)
			architecture = buf.String()
			part.Close()
		} else if formName == "file" {
			// Keep the file part open for streaming - don't close yet!
			filename = part.FileName()
			filePart = part
			hs.log.Infof("Received file part: %s", filename)
			break // Stop reading more parts
		} else {
			// Unknown field, skip it
			part.Close()
		}
	}

	// Validate required fields
	if imageName == "" || imageType == "" {
		if filePart != nil {
			filePart.Close()
		}
		http.Error(w, "imageName and imageType are required", http.StatusBadRequest)
		return
	}

	if filePart == nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer filePart.Close()

	hs.log.Infof("Upload metadata: imageName=%s, imageType=%s, architecture=%s", imageName, imageType, architecture)

	// Check storage type to determine strategy
	storageType := "pvc" // default
	if hs.cfg.ImageBuilder != nil && hs.cfg.ImageBuilder.Storage != nil {
		storageType = hs.cfg.ImageBuilder.Storage.Type
	}

	// Stream directly to final storage
	storageManager := NewStorageManager(hs.cfg, hs.log.WithField("component", "storage"))

	var storageRef *StorageReference

	if storageType == "s3" {
		// For S3: stream directly from HTTP to S3 (no temp file)
		hs.log.Info("Streaming directly to S3 storage")
		storageRef, err = storageManager.StreamToS3(r.Context(), imageName, filePart, imageType, filename)
	} else {
		// For local/PVC: stream directly to final destination (no temp file in /tmp to avoid tmpfs/RAM issues)
		hs.log.Info("Streaming directly to final storage location")
		storageRef, err = storageManager.StreamToLocalOrPVC(r.Context(), imageName, filePart, imageType)
	}

	if err != nil {
		hs.log.WithError(err).Error("Failed to store bootc image")
		http.Error(w, fmt.Sprintf("Failed to store image: %v", err), http.StatusInternalServerError)
		return
	}

	hs.log.Infof("Successfully stored bootc image: %s (type: %s, path: %s)", imageName, imageType, storageRef.Path)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"imageName":   imageName,
		"imageType":   imageType,
		"storageType": storageRef.Type,
		"storagePath": storageRef.Path,
		"size":        storageRef.Size,
	})
}

// uploadAuthMiddleware validates the upload token
func (hs *HTTPServer) uploadAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedToken := os.Getenv("UPLOAD_TOKEN")
		if expectedToken == "" {
			hs.log.Warn("UPLOAD_TOKEN not configured, upload endpoint disabled")
			http.Error(w, "Upload endpoint not configured", http.StatusServiceUnavailable)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			hs.log.Warn("Upload request without Authorization header")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			hs.log.Warn("Upload request with invalid Authorization format")
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, prefix)
		if token != expectedToken {
			hs.log.Warn("Upload request with invalid token")
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleDownload serves bootc image files from storage
func (hs *HTTPServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	filename := chi.URLParam(r, "filename")

	if name == "" || filename == "" {
		http.Error(w, "ImageBuild name and filename are required", http.StatusBadRequest)
		return
	}

	hs.log.Infof("Download request for %s/%s", name, filename)

	// Parse filename to extract image type
	// Expected format: {name}-{type}-{architecture}
	// Example: test-iso-x86_64
	parts := strings.Split(filename, "-")
	if len(parts) < 2 {
		http.Error(w, "Invalid filename format", http.StatusBadRequest)
		return
	}

	// Extract the type (last part before architecture, or second-to-last part)
	// For "test-iso-x86_64", we want "iso"
	var imageType string
	if len(parts) >= 3 {
		// Assume format: {name}-{type}-{arch}
		imageType = parts[len(parts)-2]
	} else {
		imageType = parts[len(parts)-1]
	}

	hs.log.Infof("Serving image type=%s for build=%s", imageType, name)

	// Get storage configuration
	if hs.cfg.ImageBuilder == nil || hs.cfg.ImageBuilder.Storage == nil {
		http.Error(w, "Storage not configured", http.StatusServiceUnavailable)
		return
	}

	storage := hs.cfg.ImageBuilder.Storage

	// Determine file path based on storage type
	var filePath string
	switch storage.Type {
	case "local":
		basePath := storage.LocalPath
		if basePath == "" {
			basePath = "/var/lib/flightctl/images"
		}
		filePath = filepath.Join(basePath, name, fmt.Sprintf("%s.%s", imageType, getImageExtensionFromString(imageType)))

	case "pvc":
		pvcBasePath := fmt.Sprintf("/mnt/pvc/%s", storage.PVCName)
		filePath = filepath.Join(pvcBasePath, name, fmt.Sprintf("%s.%s", imageType, getImageExtensionFromString(imageType)))

	case "s3":
		// For S3, we need to redirect or proxy to the S3 URL
		// For now, return an error indicating S3 downloads are not yet implemented
		http.Error(w, "S3 downloads not yet implemented", http.StatusNotImplemented)
		return

	default:
		http.Error(w, fmt.Sprintf("Unsupported storage type: %s", storage.Type), http.StatusInternalServerError)
		return
	}

	hs.log.Infof("Serving file: %s", filePath)

	// Check if file exists
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			hs.log.Warnf("File not found: %s", filePath)
			http.Error(w, "File not found", http.StatusNotFound)
		} else {
			hs.log.WithError(err).Errorf("Failed to stat file: %s", filePath)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		hs.log.WithError(err).Errorf("Failed to open file: %s", filePath)
		http.Error(w, "Failed to open file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Set appropriate headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// Stream the file to the client
	bytesWritten, err := io.Copy(w, file)
	if err != nil {
		hs.log.WithError(err).Errorf("Failed to stream file to client")
		return
	}

	hs.log.Infof("Successfully served %d bytes for %s/%s", bytesWritten, name, filename)
}

// getBuildNamespace returns the configured build namespace
func (hs *HTTPServer) getBuildNamespace() string {
	if hs.cfg.ImageBuilder != nil && hs.cfg.ImageBuilder.BuildNamespace != "" {
		return hs.cfg.ImageBuilder.BuildNamespace
	}
	return "flightctl-builds"
}
