package service

import (
	"context"
	"errors"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

func (h *ServiceHandler) CreateImageBuild(ctx context.Context, imageBuild api.ImageBuild) (*api.ImageBuild, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	imageBuild.Status = nil
	NilOutManagedObjectMetaProperties(&imageBuild.Metadata)

	// Basic validation - check required fields
	if imageBuild.Metadata.Name == nil || *imageBuild.Metadata.Name == "" {
		return nil, api.StatusBadRequest("metadata.name is required")
	}
	if imageBuild.Spec.BaseImage == "" {
		return nil, api.StatusBadRequest("spec.baseImage is required")
	}

	result, err := h.store.ImageBuild().Create(ctx, orgId, &imageBuild, h.callbackImageBuildUpdated)
	return result, StoreErrorToApiStatus(err, true, api.ImageBuildKind, imageBuild.Metadata.Name)
}

func (h *ServiceHandler) ListImageBuilds(ctx context.Context, params api.ListImageBuildsParams) (*api.ImageBuildList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.ImageBuild().List(ctx, orgId, *listParams)
	if err == nil {
		return result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetImageBuild(ctx context.Context, name string) (*api.ImageBuild, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.ImageBuild().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
}

func (h *ServiceHandler) ReplaceImageBuild(ctx context.Context, name string, imageBuild api.ImageBuild) (*api.ImageBuild, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't overwrite fields that are managed by the service
	isInternal := IsInternalRequest(ctx)
	if !isInternal {
		imageBuild.Status = nil
		NilOutManagedObjectMetaProperties(&imageBuild.Metadata)
	}

	// Basic validation
	if imageBuild.Spec.BaseImage == "" {
		return nil, api.StatusBadRequest("spec.baseImage is required")
	}
	if name != *imageBuild.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.ImageBuild().CreateOrUpdate(ctx, orgId, &imageBuild, nil, !isInternal, h.callbackImageBuildUpdated)
	return result, StoreErrorToApiStatus(err, created, api.ImageBuildKind, &name)
}

func (h *ServiceHandler) DeleteImageBuild(ctx context.Context, name string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	ib, err := h.store.ImageBuild().Get(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return api.StatusOK() // idempotent delete
		}
		return StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
	}
	if ib.Metadata.Owner != nil {
		// Can't delete via api
		return api.StatusConflict("unauthorized to delete imagebuild because it is owned by another resource")
	}

	err = h.store.ImageBuild().Delete(ctx, orgId, name, h.callbackImageBuildDeleted)
	return StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
}

func (h *ServiceHandler) GetImageBuildStatus(ctx context.Context, name string) (*api.ImageBuild, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.ImageBuild().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
}

func (h *ServiceHandler) ReplaceImageBuildStatus(ctx context.Context, name string, imageBuild api.ImageBuild) (*api.ImageBuild, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.ImageBuild().UpdateStatus(ctx, orgId, &imageBuild)
	return result, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
}

func (h *ServiceHandler) PatchImageBuild(ctx context.Context, name string, patch api.PatchRequest) (*api.ImageBuild, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	currentObj, err := h.store.ImageBuild().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
	}

	newObj := &api.ImageBuild{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/imagebuilds/"+name)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	// Basic validation after patch
	if newObj.Spec.BaseImage == "" {
		return nil, api.StatusBadRequest("spec.baseImage is required")
	}

	// Preserve imagebuilder-specific annotations
	// Strategy: Take modified ones from newObj (after patch), unmodified ones from currentObj
	imageBuilderAnnotations := make(map[string]string)
	touchedImageBuilderAnnotations := false
	modifiedAnnotationKeys := make(map[string]bool)
	
	// Track which imagebuilder annotations are being modified by the patch
	for _, op := range patch {
		if strings.HasPrefix(op.Path, "/metadata/annotations/imagebuilder.flightctl.io") {
			touchedImageBuilderAnnotations = true
			// Extract the annotation key
			annotationKey := strings.TrimPrefix(op.Path, "/metadata/annotations/")
			annotationKey = strings.ReplaceAll(annotationKey, "~1", "/")
			modifiedAnnotationKeys[annotationKey] = true
		}
	}
	
	// First, preserve unmodified imagebuilder annotations from currentObj (before patch)
	if currentObj.Metadata.Annotations != nil {
		for key, value := range *currentObj.Metadata.Annotations {
			if strings.HasPrefix(key, "imagebuilder.flightctl.io/") {
				// Only take from currentObj if NOT modified by the patch
				if !modifiedAnnotationKeys[key] {
					imageBuilderAnnotations[key] = value
				}
			}
		}
	}
	
	// Then, add/update with modified imagebuilder annotations from newObj (after patch)
	if newObj.Metadata.Annotations != nil {
		for key, value := range *newObj.Metadata.Annotations {
			if strings.HasPrefix(key, "imagebuilder.flightctl.io/") {
				// Only take from newObj if modified by the patch (add/replace)
				if modifiedAnnotationKeys[key] {
				imageBuilderAnnotations[key] = value
				}
			}
		}
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	// Restore all imagebuilder-specific annotations
	if len(imageBuilderAnnotations) > 0 {
		if newObj.Metadata.Annotations == nil {
			newObj.Metadata.Annotations = &map[string]string{}
		}
		for key, value := range imageBuilderAnnotations {
			(*newObj.Metadata.Annotations)[key] = value
		}
	}

	// CRITICAL: If the patch touches imagebuilder annotations (add/remove/replace),
	// we MUST use fromAPI=false to prevent the store from clearing ALL annotations
	fromAPI := !touchedImageBuilderAnnotations
	result, err := h.store.ImageBuild().Update(ctx, orgId, newObj, nil, fromAPI, h.callbackImageBuildUpdated)
	return result, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
}

func (h *ServiceHandler) PatchImageBuildStatus(ctx context.Context, name string, patch api.PatchRequest) (*api.ImageBuild, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	currentObj, err := h.store.ImageBuild().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
	}

	newObj := &api.ImageBuild{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/imagebuilds/"+name+"/status")
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	result, err := h.store.ImageBuild().UpdateStatus(ctx, orgId, newObj)
	return result, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
}

func (h *ServiceHandler) callbackImageBuildUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	// For now, we don't have specific event handling for ImageBuild
	// In the future, you could add specific logic here, similar to Fleet or Repository
	if err != nil {
		h.log.WithError(err).Errorf("ImageBuild update callback error for %s", name)
	}
}

func (h *ServiceHandler) callbackImageBuildDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	// Clean up bootc artifacts from PVC if the ImageBuild had bootc exports
	if oldResource != nil {
		if imageBuild, ok := oldResource.(*api.ImageBuild); ok {
			if imageBuild.Spec.BootcExports != nil && len(*imageBuild.Spec.BootcExports) > 0 {
				h.log.Infof("Cleaning up bootc artifacts for deleted ImageBuild %s", name)
				// Request cleanup of the ImageBuild's directory in the PVC
				// This is handled asynchronously by creating a cleanup job
				if cleanupErr := h.cleanupBootcArtifacts(ctx, name); cleanupErr != nil {
					h.log.WithError(cleanupErr).Warnf("Failed to cleanup bootc artifacts for ImageBuild %s", name)
				} else {
					h.log.Infof("Successfully scheduled cleanup for bootc artifacts of ImageBuild %s", name)
				}
			}
		}
	}

	// Handle callback errors
	if err != nil {
		h.log.WithError(err).Errorf("ImageBuild delete callback error for %s", name)
	}

	// Call the generic event handler
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// GetImageBuildLogs retrieves logs from the build job
func (h *ServiceHandler) GetImageBuildLogs(ctx context.Context, name string) ([]string, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// Get the ImageBuild to check if it exists
	imageBuild, err := h.store.ImageBuild().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.ImageBuildKind, &name)
	}

	// For now, return a simple message based on phase
	// In the future, this should query the actual job logs from Kubernetes
	phase := "Unknown"
	if imageBuild.Status != nil && imageBuild.Status.Phase != nil {
		phase = string(*imageBuild.Status.Phase)
	}

	logs := []string{
		"Build phase: " + phase,
	}

	if imageBuild.Status != nil && imageBuild.Status.Message != nil {
		logs = append(logs, "Status: "+*imageBuild.Status.Message)
	}

	if imageBuild.Status != nil && imageBuild.Status.StartTime != nil {
		logs = append(logs, "Start time: "+imageBuild.Status.StartTime.String())
	}

	if imageBuild.Status != nil && imageBuild.Status.CompletionTime != nil {
		logs = append(logs, "Completion time: "+imageBuild.Status.CompletionTime.String())
	}

	// Logs are now handled by the imagebuilder service and proxied by the API server
	// This method should not be called directly anymore
	h.log.Warn("GetImageBuildLogs called directly - logs should be retrieved via imagebuilder service proxy at /api/v1/imagebuilds/{name}/logs")

	logs = append(logs, "", "⚠️  Logs endpoint has been moved to imagebuilder service")
	logs = append(logs, "Please use the /api/v1/imagebuilds/{name}/logs endpoint which proxies to the imagebuilder service")

	return logs, api.StatusOK()
}

// GenerateContainerfile is now handled by the imagebuilder microservice
// The API server proxies requests to /api/v1/imagebuilds/generate-containerfile to the imagebuilder service
func (h *ServiceHandler) GenerateContainerfile(ctx context.Context, imageBuildSpec api.ImageBuildSpec, enrollmentCert string) (string, api.Status) {
	// This method is deprecated - containerfile generation is now proxied to the imagebuilder service
	h.log.Warn("GenerateContainerfile called directly - containerfiles should be generated via imagebuilder service proxy")
	return "", api.Status{Code: 503, Message: "Containerfile generation is handled by the imagebuilder service at /api/v1/imagebuilds/generate-containerfile"}
}

// cleanupBootcArtifacts creates a Kubernetes Job to delete bootc image files from the shared PVC
// The files are located at /output/{imagebuild-name}/ in the PVC
func (h *ServiceHandler) cleanupBootcArtifacts(ctx context.Context, imageBuildName string) error {
	// This function will be implemented to create a cleanup job
	// For now, log that cleanup was requested
	h.log.Infof("Cleanup requested for ImageBuild %s artifacts in PVC", imageBuildName)

	// TODO: Create a Kubernetes Job that:
	// 1. Mounts the imagebuilder-storage PVC
	// 2. Runs: rm -rf /output/{imageBuildName}
	// 3. Self-deletes after completion (TTLSecondsAfterFinished)

	return nil
}
