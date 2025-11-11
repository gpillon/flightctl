package store

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type ImageBuild interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild, eventCallback EventCallback) (*api.ImageBuild, error)
	Update(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*api.ImageBuild, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*api.ImageBuild, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ImageBuildList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error)
}

type ImageBuildStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.ImageBuild, model.ImageBuild, api.ImageBuild, api.ImageBuildList]
}

// Make sure we conform to ImageBuild interface
var _ ImageBuild = (*ImageBuildStore)(nil)

func NewImageBuild(db *gorm.DB, log logrus.FieldLogger) ImageBuild {
	genericStore := NewGenericStore(
		db,
		log,
		model.NewImageBuildFromApiResource,
		(*model.ImageBuild).ToApiResource,
		model.ImageBuildsToApiResource,
	)
	return &ImageBuildStore{dbHandler: db, log: log, genericStore: genericStore}
}

func (s *ImageBuildStore) callEventCallback(ctx context.Context, eventCallback EventCallback, orgId uuid.UUID, name string, oldImageBuild, newImageBuild *api.ImageBuild, created bool, err error) {
	if eventCallback == nil {
		return
	}

	SafeEventCallback(s.log, func() {
		eventCallback(ctx, api.ImageBuildKind, orgId, name, oldImageBuild, newImageBuild, created, err)
	})
}

func (s *ImageBuildStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *ImageBuildStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.ImageBuild{}); err != nil {
		return err
	}

	// Create GIN index for ImageBuild labels
	if !db.Migrator().HasIndex(&model.ImageBuild{}, "idx_imagebuild_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_imagebuild_labels ON image_builds USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.ImageBuild{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for ImageBuild annotations
	if !db.Migrator().HasIndex(&model.ImageBuild{}, "idx_imagebuild_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_imagebuild_annotations ON image_builds USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.ImageBuild{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *ImageBuildStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.ImageBuild, eventCallback EventCallback) (*api.ImageBuild, error) {
	imageBuild, err := s.genericStore.Create(ctx, orgId, resource)
	name := lo.FromPtr(resource.Metadata.Name)
	s.callEventCallback(ctx, eventCallback, orgId, name, nil, imageBuild, true, err)
	return imageBuild, err
}

func (s *ImageBuildStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.ImageBuild, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*api.ImageBuild, error) {
	newImageBuild, oldImageBuild, err := s.genericStore.Update(ctx, orgId, resource, fieldsToUnset, fromAPI, nil)
	s.callEventCallback(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldImageBuild, newImageBuild, false, err)
	return newImageBuild, err
}

func (s *ImageBuildStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.ImageBuild, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*api.ImageBuild, bool, error) {
	newImageBuild, oldImageBuild, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, nil)
	s.callEventCallback(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldImageBuild, newImageBuild, created, err)
	return newImageBuild, created, err
}

func (s *ImageBuildStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *ImageBuildStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ImageBuildList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *ImageBuildStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error {
	oldImageBuild, err := s.Get(ctx, orgId, name)
	if err != nil {
		s.callEventCallback(ctx, eventCallback, orgId, name, oldImageBuild, nil, false, err)
		return err
	}

	// Use the generic store's delete method
	db := s.getDB(ctx)
	result := db.Unscoped().Where("org_id = ? AND name = ?", orgId, name).Delete(&model.ImageBuild{})
	if result.Error != nil {
		s.callEventCallback(ctx, eventCallback, orgId, name, oldImageBuild, nil, false, result.Error)
		return result.Error
	}

	s.callEventCallback(ctx, eventCallback, orgId, name, oldImageBuild, nil, false, nil)
	return nil
}

func (s *ImageBuildStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ImageBuild) (*api.ImageBuild, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}
