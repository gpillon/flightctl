package imagebuilder_test

import (
	"context"
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var (
	suiteCtx context.Context
)

func TestImageBuilder(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ImageBuilder Store Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("ImageBuilder Integration Suite")
})

var _ = Describe("ImageBuild Store", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		ibStore   store.ImageBuild
		cfg       *config.Config
		dbName    string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		ibStore = storeInst.ImageBuild()

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	Context("Create ImageBuild", func() {
		It("should create a new ImageBuild successfully", func() {
			imageBuild := api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-build"),
					Labels: &map[string]string{
						"env": "test",
					},
				},
				Spec: api.ImageBuildSpec{
					BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
					Customizations: &api.ImageBuildCustomizations{
						Packages: &[]string{"vim", "curl"},
					},
				},
			}

			result, created, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal("test-build"))
			Expect(result.Spec.BaseImage).To(Equal("quay.io/centos-bootc/centos-bootc:stream9"))
		})

		It("should fail to create ImageBuild without required baseImage", func() {
			imageBuild := api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("invalid-build"),
				},
				Spec: api.ImageBuildSpec{
					// Missing required BaseImage
				},
			}

			_, _, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
			Expect(err).To(HaveOccurred())
		})

		It("should create ImageBuild with full customizations", func() {
			imageBuild := api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("full-build"),
				},
				Spec: api.ImageBuildSpec{
					BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
					Customizations: &api.ImageBuildCustomizations{
						Packages: &[]string{"vim", "curl", "git"},
						Files: &[]api.ImageBuildFile{
							{
								Path:    "/etc/test.conf",
								Content: "test content",
								Mode:    lo.ToPtr("0644"),
							},
						},
						Users: &[]api.ImageBuildUser{
							{
								Name:   "testuser",
								Groups: &[]string{"wheel"},
							},
						},
						EnableEpel: lo.ToPtr(true),
					},
					BootcExports: &[]api.BootcExport{
						{
							Type:         api.BootcExportType("qcow2"),
							Architecture: lo.ToPtr(api.BootcExportArchitecture("x86_64")),
						},
					},
				},
			}

			result, created, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(result.Spec.Customizations.Packages).ToNot(BeNil())
			Expect(*result.Spec.Customizations.Packages).To(HaveLen(3))
			Expect(result.Spec.BootcExports).ToNot(BeNil())
			Expect(*result.Spec.BootcExports).To(HaveLen(1))
		})
	})

	Context("Get ImageBuild", func() {
		BeforeEach(func() {
			// Create test ImageBuilds
			for i := 0; i < 3; i++ {
				name := fmt.Sprintf("test-build-%d", i)
				imageBuild := api.ImageBuild{
					Metadata: api.ObjectMeta{
						Name: &name,
					},
					Spec: api.ImageBuildSpec{
						BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
					},
				}
				_, _, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
				Expect(err).ToNot(HaveOccurred())
			}
		})

		It("should get an existing ImageBuild by name", func() {
			name := "test-build-0"
			result, err := ibStore.Get(ctx, orgId, name)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(*result.Metadata.Name).To(Equal(name))
		})

		It("should return error for non-existent ImageBuild", func() {
			_, err := ibStore.Get(ctx, orgId, "non-existent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should list all ImageBuilds", func() {
			listParams := store.ListParams{}
			result, err := ibStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(3))
		})

		It("should list ImageBuilds with label selector", func() {
			// Create ImageBuild with specific label
			imageBuild := api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("labeled-build"),
					Labels: &map[string]string{
						"environment": "production",
					},
				},
				Spec: api.ImageBuildSpec{
					BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
				},
			}
			_, _, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{
				LabelSelector: selector.NewLabelSelectorFromMapOrDie(map[string]string{"environment": "production"}),
			}
			result, err := ibStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(1))
			Expect(*result.Items[0].Metadata.Name).To(Equal("labeled-build"))
		})
	})

	Context("Update ImageBuild", func() {
		var testBuildName string

		BeforeEach(func() {
			testBuildName = "update-test-build"
			imageBuild := api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: &testBuildName,
				},
				Spec: api.ImageBuildSpec{
					BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
				},
			}
			_, _, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should update an existing ImageBuild", func() {
			// Get current ImageBuild
			current, err := ibStore.Get(ctx, orgId, testBuildName)
			Expect(err).ToNot(HaveOccurred())

			// Update it
			current.Spec.Customizations = &api.ImageBuildCustomizations{
				Packages: &[]string{"updated-package"},
			}

			result, created, err := ibStore.CreateOrUpdate(ctx, orgId, current, nil, false, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(result.Spec.Customizations.Packages).ToNot(BeNil())
			Expect(*result.Spec.Customizations.Packages).To(ContainElement("updated-package"))
		})

		It("should update ImageBuild status", func() {
			// Get current ImageBuild
			current, err := ibStore.Get(ctx, orgId, testBuildName)
			Expect(err).ToNot(HaveOccurred())

			// Update status
			phase := api.ImageBuildStatusPhase("Building")
			current.Status = &api.ImageBuildStatus{
				Phase:   &phase,
				Message: lo.ToPtr("Build in progress"),
			}

			result, err := ibStore.UpdateStatus(ctx, orgId, current)
			Expect(err).ToNot(HaveOccurred())

			// Verify status was updated
			Expect(result.Status).ToNot(BeNil())
			Expect(string(*result.Status.Phase)).To(Equal("Building"))
			Expect(*result.Status.Message).To(Equal("Build in progress"))
		})
	})

	Context("Delete ImageBuild", func() {
		var testBuildName string

		BeforeEach(func() {
			testBuildName = "delete-test-build"
			imageBuild := api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: &testBuildName,
				},
				Spec: api.ImageBuildSpec{
					BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
				},
			}
			_, _, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should delete an existing ImageBuild", func() {
			err := ibStore.Delete(ctx, orgId, testBuildName, nil)
			Expect(err).ToNot(HaveOccurred())

			// Verify it's deleted
			_, err = ibStore.Get(ctx, orgId, testBuildName)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return error when deleting non-existent ImageBuild", func() {
			err := ibStore.Delete(ctx, orgId, "non-existent", nil)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("ImageBuild with ContainerRegistry configuration", func() {
		It("should create ImageBuild with registry credentials", func() {
			imageBuild := api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("registry-build"),
				},
				Spec: api.ImageBuildSpec{
					BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
					ContainerRegistry: &api.ContainerRegistryConfig{
						Url: "quay.io/myrepo/myimage:latest",
						Credentials: &struct {
							Password *string `json:"password,omitempty"`
							Username *string `json:"username,omitempty"`
						}{
							Username: lo.ToPtr("testuser"),
							Password: lo.ToPtr("testpass"),
						},
					},
					PushToRegistry: lo.ToPtr(true),
				},
			}

			result, created, err := ibStore.CreateOrUpdate(ctx, orgId, &imageBuild, nil, false, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(result.Spec.ContainerRegistry).ToNot(BeNil())
			Expect(result.Spec.ContainerRegistry.Url).To(Equal("quay.io/myrepo/myimage:latest"))
			Expect(*result.Spec.PushToRegistry).To(BeTrue())
		})
	})
})
