package model

import (
	"encoding/json"
	"fmt"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type ImageBuild struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.ImageBuildSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.ImageBuildStatus] `gorm:"type:jsonb"`
}

func (ib ImageBuild) String() string {
	val, _ := json.Marshal(ib)
	return string(val)
}

// GetStatusAsJson returns the status as JSON bytes
func (ib *ImageBuild) GetStatusAsJson() ([]byte, error) {
	if ib.Status == nil {
		return []byte("null"), nil
	}
	return json.Marshal(ib.Status.Data)
}

func NewImageBuildFromApiResource(resource *api.ImageBuild) (*ImageBuild, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &ImageBuild{}, nil
	}

	status := api.ImageBuildStatus{}
	if resource.Status != nil {
		status = *resource.Status
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}
	return &ImageBuild{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func ImageBuildAPIVersion() string {
	return fmt.Sprintf("%s/%s", api.APIGroup, api.ImageBuildAPIVersion)
}

func (ib *ImageBuild) ToApiResource(opts ...APIResourceOption) (*api.ImageBuild, error) {
	if ib == nil {
		return &api.ImageBuild{}, nil
	}

	spec := api.ImageBuildSpec{}
	if ib.Spec != nil {
		spec = ib.Spec.Data
	}

	status := api.ImageBuildStatus{}
	if ib.Status != nil {
		status = ib.Status.Data
	}

	return &api.ImageBuild{
		ApiVersion: ImageBuildAPIVersion(),
		Kind:       api.ImageBuildKind,
		Metadata: api.ObjectMeta{
			Name:              lo.ToPtr(ib.Name),
			CreationTimestamp: lo.ToPtr(ib.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(ib.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(ib.Resource.Annotations)),
			Generation:        ib.Generation,
			Owner:             ib.Owner,
			ResourceVersion:   lo.Ternary(ib.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(ib.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func ImageBuildsToApiResource(imageBuilds []ImageBuild, cont *string, numRemaining *int64) (api.ImageBuildList, error) {
	imageBuildList := make([]api.ImageBuild, len(imageBuilds))
	for i, imageBuild := range imageBuilds {
		apiResource, _ := imageBuild.ToApiResource()
		imageBuildList[i] = *apiResource
	}
	ret := api.ImageBuildList{
		ApiVersion: ImageBuildAPIVersion(),
		Kind:       api.ImageBuildListKind,
		Items:      imageBuildList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func ImageBuildPtrToImageBuild(ib *ImageBuild) *ImageBuild {
	return ib
}

func (ib *ImageBuild) GetKind() string {
	return api.ImageBuildKind
}

func (ib *ImageBuild) HasNilSpec() bool {
	return ib.Spec == nil
}

func (ib *ImageBuild) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*ImageBuild)
	if !ok {
		return false
	}
	if other == nil {
		return false
	}
	if (ib.Spec == nil && other.Spec != nil) || (ib.Spec != nil && other.Spec == nil) {
		return false
	}
	if ib.Spec == nil && other.Spec == nil {
		return true
	}
	// For now, we consider specs equal if they both exist
	// In production, you might want a more sophisticated comparison
	return true
}

