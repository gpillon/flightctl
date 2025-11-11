package imagebuilder

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

// StorageManager handles storing bootc images to various storage backends
type StorageManager struct {
	config *config.Config
	log    logrus.FieldLogger
}

// NewStorageManager creates a new storage manager
func NewStorageManager(cfg *config.Config, log logrus.FieldLogger) *StorageManager {
	return &StorageManager{
		config: cfg,
		log:    log,
	}
}

// StorageReference represents a reference to stored image
type StorageReference struct {
	Type     string // "local", "pvc", "s3"
	Path     string // Full path or S3 URL
	Size     int64
	Metadata map[string]string
}

// StoreBootcImage stores a bootc image to the configured storage backend
func (sm *StorageManager) StoreBootcImage(ctx context.Context, imageName string, sourceFile string, imageType string) (*StorageReference, error) {
	// Check if sourceFile indicates the artifact was already uploaded by the build job
	// Format: "uploaded:{imageName}/{imageType}"
	if strings.HasPrefix(sourceFile, "uploaded:") {
		sm.log.Infof("Artifact %s was already uploaded to storage by build job, retrieving storage reference", sourceFile)
		
		// The artifact was already uploaded by the bootc-builder job via HTTP POST
		// Just return a reference to the already-stored file
		if sm.config.ImageBuilder == nil || sm.config.ImageBuilder.Storage == nil {
			return nil, fmt.Errorf("storage configuration not found")
		}
		
		storage := sm.config.ImageBuilder.Storage
		
		// Construct the storage path based on storage type
		var storagePath string
		switch storage.Type {
		case "local":
			basePath := storage.LocalPath
			if basePath == "" {
				basePath = "/var/lib/flightctl/images"
			}
			storagePath = filepath.Join(basePath, imageName, fmt.Sprintf("%s.%s", imageType, getImageExtensionFromString(imageType)))
		case "pvc":
			pvcBasePath := fmt.Sprintf("/mnt/pvc/%s", storage.PVCName)
			storagePath = filepath.Join(pvcBasePath, imageName, fmt.Sprintf("%s.%s", imageType, getImageExtensionFromString(imageType)))
		case "s3":
			storagePath = fmt.Sprintf("s3://%s/%s/%s.%s", storage.S3Config.Bucket, imageName, imageType, getImageExtensionFromString(imageType))
		default:
			return nil, fmt.Errorf("unsupported storage type: %s", storage.Type)
		}
		
		// Get file size if available (for local/pvc storage)
		var size int64
		if storage.Type == "local" || storage.Type == "pvc" {
			if fileInfo, err := os.Stat(storagePath); err == nil {
				size = fileInfo.Size()
			}
		}
		
		return &StorageReference{
			Type: storage.Type,
			Path: storagePath,
			Size: size,
			Metadata: map[string]string{
				"image_name": imageName,
				"image_type": imageType,
				"uploaded":   "true",
			},
		}, nil
	}
	
	// Legacy path: sourceFile is an actual file path that needs to be copied
	if sm.config.ImageBuilder == nil || sm.config.ImageBuilder.Storage == nil {
		return nil, fmt.Errorf("storage configuration not found")
	}

	storage := sm.config.ImageBuilder.Storage

	switch storage.Type {
	case "local":
		return sm.storeToLocal(ctx, imageName, sourceFile, imageType, storage.LocalPath)
	case "pvc":
		return sm.storeToPVC(ctx, imageName, sourceFile, imageType, storage.PVCName)
	case "s3":
		// Convert config
		s3cfg := &S3ConfigType{
			Endpoint:  storage.S3Config.Endpoint,
			Bucket:    storage.S3Config.Bucket,
			Region:    storage.S3Config.Region,
			AccessKey: storage.S3Config.AccessKey,
			SecretKey: []byte(storage.S3Config.SecretKey),
		}
		return sm.storeToS3(ctx, imageName, sourceFile, imageType, s3cfg)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storage.Type)
	}
}

// storeToLocal stores the image to a local filesystem path
func (sm *StorageManager) storeToLocal(ctx context.Context, imageName, sourceFile, imageType, basePath string) (*StorageReference, error) {
	if basePath == "" {
		basePath = "/var/lib/flightctl/images"
	}

	// Create directory structure
	targetDir := filepath.Join(basePath, imageName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	// Copy file
	targetFile := filepath.Join(targetDir, fmt.Sprintf("%s.%s", imageType, getImageExtensionFromString(imageType)))
	
	sm.log.Infof("Storing image to local filesystem: %s", targetFile)

	size, err := copyFile(sourceFile, targetFile)
	if err != nil {
		return nil, fmt.Errorf("failed to copy file: %w", err)
	}

	return &StorageReference{
		Type: "local",
		Path: targetFile,
		Size: size,
		Metadata: map[string]string{
			"image_name": imageName,
			"image_type": imageType,
		},
	}, nil
}

// storeToPVC stores the image to a PersistentVolumeClaim
// In Kubernetes, the PVC is already mounted, so this is similar to local storage
func (sm *StorageManager) storeToPVC(ctx context.Context, imageName, sourceFile, imageType, pvcName string) (*StorageReference, error) {
	// In a real Kubernetes environment, the PVC would be mounted at a known path
	// For example: /mnt/pvc/<pvcName>
	pvcBasePath := fmt.Sprintf("/mnt/pvc/%s", pvcName)

	sm.log.Infof("Storing image to PVC %s at %s", pvcName, pvcBasePath)

	// Create directory structure
	targetDir := filepath.Join(pvcBasePath, imageName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory in PVC: %w", err)
	}

	// Copy file
	targetFile := filepath.Join(targetDir, fmt.Sprintf("%s.%s", imageType, getImageExtensionFromString(imageType)))
	
	size, err := copyFile(sourceFile, targetFile)
	if err != nil {
		return nil, fmt.Errorf("failed to copy file to PVC: %w", err)
	}

	return &StorageReference{
		Type: "pvc",
		Path: targetFile,
		Size: size,
		Metadata: map[string]string{
			"image_name": imageName,
			"image_type": imageType,
			"pvc_name":   pvcName,
		},
	}, nil
}

// S3ConfigType represents S3 storage configuration
type S3ConfigType struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey []byte
}

// StreamToLocalOrPVC streams data directly to local or PVC storage (no temp file in /tmp)
func (sm *StorageManager) StreamToLocalOrPVC(ctx context.Context, imageName string, reader io.Reader, imageType string) (*StorageReference, error) {
	if sm.config.ImageBuilder == nil || sm.config.ImageBuilder.Storage == nil {
		return nil, fmt.Errorf("storage configuration not found")
	}

	storage := sm.config.ImageBuilder.Storage
	
	var basePath string
	var storageType string
	
	switch storage.Type {
	case "local":
		storageType = "local"
		basePath = storage.LocalPath
		if basePath == "" {
			basePath = "/var/lib/flightctl/images"
		}
	case "pvc":
		storageType = "pvc"
		basePath = fmt.Sprintf("/mnt/pvc/%s", storage.PVCName)
	default:
		return nil, fmt.Errorf("unsupported storage type for streaming: %s", storage.Type)
	}

	// Create directory structure
	targetDir := filepath.Join(basePath, imageName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	// Create final file directly (no temp file)
	targetFile := filepath.Join(targetDir, fmt.Sprintf("%s.%s", imageType, getImageExtensionFromString(imageType)))
	
	sm.log.Infof("Streaming directly to final location: %s", targetFile)

	// Create/open the final file
	file, err := os.Create(targetFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create target file: %w", err)
	}

	// Stream directly to final file
	bytesWritten, err := io.Copy(file, reader)
	file.Close()

	if err != nil {
		// If streaming failed, delete the partial file
		os.Remove(targetFile)
		return nil, fmt.Errorf("failed to stream to file: %w", err)
	}

	// Ensure data is flushed to disk
	// Note: file.Close() already called above, which flushes buffers
	
	sm.log.Infof("Successfully streamed %d bytes directly to %s", bytesWritten, targetFile)

	return &StorageReference{
		Type: storageType,
		Path: targetFile,
		Size: bytesWritten,
		Metadata: map[string]string{
			"image_name": imageName,
			"image_type": imageType,
			"streamed":   "true",
		},
	}, nil
}

// StreamToS3 streams data directly from an io.Reader to S3 (no temp file)
func (sm *StorageManager) StreamToS3(ctx context.Context, imageName string, reader io.Reader, imageType, filename string) (*StorageReference, error) {
	if sm.config.ImageBuilder == nil || sm.config.ImageBuilder.Storage == nil || sm.config.ImageBuilder.Storage.S3Config == nil {
		return nil, fmt.Errorf("S3 configuration not found")
	}

	s3cfg := sm.config.ImageBuilder.Storage.S3Config
	s3Config := &S3ConfigType{
		Endpoint:  s3cfg.Endpoint,
		Bucket:    s3cfg.Bucket,
		Region:    s3cfg.Region,
		AccessKey: s3cfg.AccessKey,
		SecretKey: []byte(s3cfg.SecretKey),
	}

	sm.log.Infof("Streaming directly to S3 bucket %s", s3Config.Bucket)

	// Create AWS session
	awsConfig := &aws.Config{
		Endpoint:         aws.String(s3Config.Endpoint),
		Region:           aws.String(getS3Region(s3Config.Region)),
		Credentials:      credentials.NewStaticCredentials(s3Config.AccessKey, string(s3Config.SecretKey), ""),
		S3ForcePathStyle: aws.Bool(true), // For compatibility with MinIO and other S3-compatible services
	}

	sess, err := session.NewSession(awsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %w", err)
	}

	s3Client := s3.New(sess)

	// Create S3 key (object path)
	s3Key := fmt.Sprintf("%s/%s.%s", imageName, imageType, getImageExtensionFromString(imageType))

	// Upload to S3 - stream directly from reader (no buffering in memory)
	// AWS SDK will handle chunked upload automatically
	result, err := s3Client.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s3Config.Bucket),
		Key:    aws.String(s3Key),
		Body:   aws.ReadSeekCloser(reader), // Wrap reader for AWS SDK
		Metadata: map[string]*string{
			"image-name": aws.String(imageName),
			"image-type": aws.String(imageType),
			"filename":   aws.String(filename),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload to S3: %w", err)
	}

	s3URL := fmt.Sprintf("s3://%s/%s", s3Config.Bucket, s3Key)
	sm.log.Infof("Successfully streamed image to %s (ETag: %s)", s3URL, aws.StringValue(result.ETag))

	// We don't know the exact size when streaming, but we can note it was streamed
	return &StorageReference{
		Type: "s3",
		Path: s3URL,
		Size: 0, // Unknown when streaming
		Metadata: map[string]string{
			"image_name": imageName,
			"image_type": imageType,
			"bucket":     s3Config.Bucket,
			"key":        s3Key,
			"etag":       aws.StringValue(result.ETag),
			"streamed":   "true",
		},
	}, nil
}

// storeToS3 stores the image to an S3-compatible object storage
func (sm *StorageManager) storeToS3(ctx context.Context, imageName, sourceFile, imageType string, s3Config *S3ConfigType) (*StorageReference, error) {
	if s3Config == nil {
		return nil, fmt.Errorf("S3 configuration not provided")
	}

	sm.log.Infof("Storing image to S3 bucket %s", s3Config.Bucket)

	// Create AWS session
	awsConfig := &aws.Config{
		Endpoint:         aws.String(s3Config.Endpoint),
		Region:           aws.String(getS3Region(s3Config.Region)),
		Credentials:      credentials.NewStaticCredentials(s3Config.AccessKey, string(s3Config.SecretKey), ""),
		S3ForcePathStyle: aws.Bool(true), // For compatibility with MinIO and other S3-compatible services
	}

	sess, err := session.NewSession(awsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %w", err)
	}

	s3Client := s3.New(sess)

	// Open source file
	file, err := os.Open(sourceFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open source file: %w", err)
	}
	defer file.Close()

	// Get file info for size
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat source file: %w", err)
	}

	// Create S3 key (object path)
	s3Key := fmt.Sprintf("%s/%s.%s", imageName, imageType, getImageExtensionFromString(imageType))

	// Upload to S3
	_, err = s3Client.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s3Config.Bucket),
		Key:    aws.String(s3Key),
		Body:   file,
		Metadata: map[string]*string{
			"image-name": aws.String(imageName),
			"image-type": aws.String(imageType),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload to S3: %w", err)
	}

	s3URL := fmt.Sprintf("s3://%s/%s", s3Config.Bucket, s3Key)
	sm.log.Infof("Successfully uploaded image to %s", s3URL)

	return &StorageReference{
		Type: "s3",
		Path: s3URL,
		Size: fileInfo.Size(),
		Metadata: map[string]string{
			"image_name": imageName,
			"image_type": imageType,
			"bucket":     s3Config.Bucket,
			"key":        s3Key,
		},
	}, nil
}

// copyFile copies a file from source to destination
func copyFile(source, destination string) (int64, error) {
	sourceFile, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(destination)
	if err != nil {
		return 0, err
	}
	defer destFile.Close()

	size, err := io.Copy(destFile, sourceFile)
	if err != nil {
		return 0, err
	}

	// Sync to ensure data is written to disk
	err = destFile.Sync()
	if err != nil {
		return 0, err
	}

	return size, nil
}

// getS3Region returns the S3 region, defaulting to us-east-1
func getS3Region(region string) string {
	if region == "" {
		return "us-east-1"
	}
	return region
}

// DeleteImage deletes an image from storage
func (sm *StorageManager) DeleteImage(ctx context.Context, ref *StorageReference) error {
	switch ref.Type {
	case "local", "pvc":
		sm.log.Infof("Deleting local/PVC image: %s", ref.Path)
		return os.Remove(ref.Path)

	case "s3":
		if sm.config.ImageBuilder == nil || sm.config.ImageBuilder.Storage == nil || sm.config.ImageBuilder.Storage.S3Config == nil {
			return fmt.Errorf("S3 configuration not found")
		}

		s3cfg := sm.config.ImageBuilder.Storage.S3Config
		s3Config := &S3ConfigType{
			Endpoint:  s3cfg.Endpoint,
			Bucket:    s3cfg.Bucket,
			Region:    s3cfg.Region,
			AccessKey: s3cfg.AccessKey,
			SecretKey: []byte(s3cfg.SecretKey),
		}
		
		// Extract bucket and key from metadata
		bucket := ref.Metadata["bucket"]
		key := ref.Metadata["key"]

		if bucket == "" || key == "" {
			return fmt.Errorf("invalid S3 reference: missing bucket or key")
		}

		sm.log.Infof("Deleting S3 image: s3://%s/%s", bucket, key)

		// Create AWS session
		awsConfig := &aws.Config{
			Endpoint:         aws.String(s3Config.Endpoint),
			Region:           aws.String(getS3Region(s3Config.Region)),
			Credentials:      credentials.NewStaticCredentials(s3Config.AccessKey, string(s3Config.SecretKey), ""),
			S3ForcePathStyle: aws.Bool(true),
		}

		sess, err := session.NewSession(awsConfig)
		if err != nil {
			return fmt.Errorf("failed to create AWS session: %w", err)
		}

		s3Client := s3.New(sess)

		_, err = s3Client.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("failed to delete from S3: %w", err)
		}

		return nil

	default:
		return fmt.Errorf("unsupported storage type: %s", ref.Type)
	}
}

