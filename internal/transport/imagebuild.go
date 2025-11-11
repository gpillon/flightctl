package transport

import (
	"context"
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// (POST /api/v1/imagebuilds)
func (h *TransportHandler) CreateImageBuild(w http.ResponseWriter, r *http.Request) {
	var imageBuild api.ImageBuild
	if err := json.NewDecoder(r.Body).Decode(&imageBuild); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.CreateImageBuild(r.Context(), imageBuild)
	SetResponse(w, body, status)
}

// (GET /api/v1/imagebuilds)
func (h *TransportHandler) ListImageBuilds(w http.ResponseWriter, r *http.Request, params api.ListImageBuildsParams) {
	body, status := h.serviceHandler.ListImageBuilds(r.Context(), params)
	SetResponse(w, body, status)
}

// (GET /api/v1/imagebuilds/{name})
func (h *TransportHandler) GetImageBuild(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetImageBuild(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/imagebuilds/{name})
func (h *TransportHandler) ReplaceImageBuild(w http.ResponseWriter, r *http.Request, name string) {
	var imageBuild api.ImageBuild
	if err := json.NewDecoder(r.Body).Decode(&imageBuild); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceImageBuild(r.Context(), name, imageBuild)
	SetResponse(w, body, status)
}

// (DELETE /api/v1/imagebuilds/{name})
func (h *TransportHandler) DeleteImageBuild(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteImageBuild(r.Context(), name)
	SetResponse(w, nil, status)
}

// (GET /api/v1/imagebuilds/{name}/status)
func (h *TransportHandler) GetImageBuildStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetImageBuildStatus(r.Context(), name)
	SetResponse(w, body, status)
}

// (PUT /api/v1/imagebuilds/{name}/status)
func (h *TransportHandler) ReplaceImageBuildStatus(w http.ResponseWriter, r *http.Request, name string) {
	var imageBuild api.ImageBuild
	if err := json.NewDecoder(r.Body).Decode(&imageBuild); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.ReplaceImageBuildStatus(r.Context(), name, imageBuild)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/imagebuilds/{name})
func (h *TransportHandler) PatchImageBuild(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchImageBuild(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// (PATCH /api/v1/imagebuilds/{name}/status)
func (h *TransportHandler) PatchImageBuildStatus(w http.ResponseWriter, r *http.Request, name string) {
	var patch api.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	body, status := h.serviceHandler.PatchImageBuildStatus(r.Context(), name, patch)
	SetResponse(w, body, status)
}

// GetImageBuildLogs retrieves logs for an ImageBuild
func (h *TransportHandler) GetImageBuildLogs(ctx context.Context, name string) ([]string, api.Status) {
	return h.serviceHandler.GetImageBuildLogs(ctx, name)
}

// GenerateContainerfile generates a Containerfile from an ImageBuildSpec
func (h *TransportHandler) GenerateContainerfile(ctx context.Context, imageBuildSpec api.ImageBuildSpec, enrollmentCert string) (string, api.Status) {
	return h.serviceHandler.GenerateContainerfile(ctx, imageBuildSpec, enrollmentCert)
}

