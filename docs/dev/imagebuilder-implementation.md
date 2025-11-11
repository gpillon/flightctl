# ImageBuilder Service Implementation

## Overview

The ImageBuilder service is a new microservice in the FlightCtl ecosystem responsible for building container images and bootc disk images from user specifications. It automates the entire image build pipeline, from certificate generation to storage management.

## Architecture

### Components

1. **API Layer** (`api/v1alpha1/openapi.yaml`)
   - `ImageBuild` resource with full Kubernetes-style metadata, spec, and status
   - CRUD endpoints: Create, Read, List, Delete, Update
   - Status subresource for tracking build progress

2. **Database Layer**
   - Model: `internal/store/model/imagebuild.go`
   - Store: `internal/store/imagebuild.go`
   - Migration: `db/migrations/0025_imagebuilds.up.sql`

3. **Service Layer**
   - Service handlers: `internal/service/imagebuild.go`
   - HTTP transport: `internal/transport/imagebuild.go`

4. **ImageBuilder Package** (`internal/imagebuilder/`)
   - **build_manager.go**: Polls for pending builds and manages their lifecycle
   - **cert_manager.go**: Requests enrollment certificates via API
   - **cleanup_manager.go**: Cleans up orphaned Kubernetes resources (Jobs, ConfigMaps) with distributed locking
   - **containerfile_generator.go**: Generates Containerfiles from user specifications
   - **container_builder.go**: Builds and pushes container images using Kubernetes Jobs
   - **bootc_builder.go**: Creates bootc disk images (ISO, QCOW2, VMDK, etc.)
   - **storage_manager.go**: Manages storage (local/PVC/S3)
   - **http_server.go**: Provides HTTP endpoints for logs, downloads, and Containerfile generation
   - **registry_utils.go**: Utilities for handling registry URLs and image references
   - **orchestrator.go**: Coordinates the entire build process

5. **ImageBuilder Service** (`cmd/flightctl-imagebuilder/`)
   - Main service that runs the BuildManager for polling pending builds
   - Runs CleanupManager on startup to remove orphaned resources
   - Provides HTTP API server for logs, downloads, and Containerfile generation
   - Triggers build orchestrator for new ImageBuild resources
   - Handles build cancellation via deletion annotations

## Build Process

The build process follows these steps:

### 1. Enrollment Certificate Request
- Automatically requests an enrollment certificate from the FlightCtl API
- Uses the `CertificateSigningRequest` API (not the CLI executable)
- Waits for certificate approval and retrieval
- Embeds certificate in the final image configuration

### 2. Containerfile Generation
The `ContainerfileGenerator` creates a complete Containerfile from the ImageBuild spec:

**Supported Customizations:**
- **Base Image**: Any OCI-compliant base image
- **COPR Repositories**: Enable additional package repositories
- **Packages**: Install additional RPM packages
- **Users**: Create users with shells, groups, passwords, and SSH keys
- **Files**: Add custom files to the image
- **Systemd Units**: Install and enable systemd services
- **Scripts**: Execute custom scripts during build
- **SSH Keys**: Add SSH authorized keys for root and users
- **FlightCtl Configuration**: Complete agent configuration including:
  - Enrollment service configuration
  - Agent intervals (spec fetch, status update)
  - Default labels
  - System info collection
  - Timeouts and log levels
  - TPM configuration

**Key Features:**
- Uses base64 encoding for reliable content transfer
- Handles special characters safely
- Creates proper directory structures
- Sets correct permissions

### 3. Container Image Build
The `ContainerBuilder` creates a Kubernetes Job that:
- Uses `buildah` to build the container image
- Mounts the generated Containerfile as a ConfigMap
- Handles registry authentication via Secrets
- Supports pushing to any OCI-compliant registry
- Configurable resource limits (CPU, memory)

### 4. Bootc Disk Image Generation
If the user requests disk images, the `BootcBuilder`:
- Creates Kubernetes Jobs using `bootc-image-builder`
- Supports multiple image types:
  - ISO (installation media)
  - QCOW2 (QEMU/KVM virtual machines)
  - VMDK (VMware)
  - RAW (bare metal)
  - AMI (AWS)
  - TAR (archives)
- Supports multiple architectures (x86_64, aarch64)
- Uses PersistentVolumeClaims for output storage

### 5. Storage Management
The `StorageManager` handles three storage backends:

**Local Storage:**
- Stores images in `/var/lib/flightctl/images/<image-name>/`
- Suitable for development and single-node deployments

**PVC Storage:**
- Stores images in Kubernetes PersistentVolumeClaims
- Mounted at `/mnt/pvc/<pvc-name>/`
- Suitable for Kubernetes/OpenShift deployments

**S3 Storage:**
- Stores images in S3-compatible object storage
- Supports AWS S3, MinIO, and other S3-compatible services
- Includes metadata for easy retrieval

### 6. Build Lifecycle Management

The `BuildManager` continuously polls for ImageBuilds and manages their lifecycle:
- Processes pending builds every 10 seconds
- Triggers the orchestrator for new builds
- Handles retry requests via annotation
- Handles cancellation requests by deleting Kubernetes Jobs
- Tracks recently cancelled builds to avoid duplicate processing

### 7. Resource Cleanup

The `CleanupManager` handles orphaned resources:
- Runs on service startup
- Uses distributed locking (via ConfigMap) to prevent race conditions
- Cleans up Jobs and ConfigMaps that no longer have corresponding ImageBuilds
- Preserves resources for active builds

### 8. HTTP API Endpoints

The `HTTPServer` provides additional endpoints:
- **`GET /healthz`**: Health check endpoint
- **`GET /api/v1/imagebuilds/{name}/logs`**: Stream build job logs
- **`GET /api/v1/imagebuilds/{name}/downloads/{filename}`**: Download build artifacts
- **`POST /api/v1/imagebuilds/generate-containerfile`**: Generate Containerfile preview
- **`POST /api/v1/imagebuilds/upload`**: Upload build artifacts (internal use)

The FlightCtl API server proxies the logs, downloads, and generate-containerfile endpoints to the imagebuilder service.

### 9. Status Updates
Throughout the build process, the orchestrator updates the ImageBuild status:
- **Pending**: Initial state when created
- **Building**: Generating Containerfile and building container image
- **Pushing**: Pushing container image to registry
- **GeneratingImages**: Building bootc disk images
- **Completed**: All builds successful
- **Failed**: Build encountered an error
- **Cancelled**: Build was cancelled via annotation

## Configuration

Add to `flightctl-builder` config file:

```yaml
imagebuilder:
  enabled: true                      # Enable/disable imagebuilder service
  buildNamespace: "flightctl-builds" # Kubernetes namespace for build jobs
  defaultRegistry: "quay.io/myorg"   # Default container registry
  serviceUrl: "http://flightctl-imagebuilder:9090"  # Service URL for API proxy
  storage:
    type: "pvc"  # "local", "pvc", or "s3"
    
    # For local storage:
    localPath: "/var/lib/flightctl/images"
    
    # For PVC storage:
    pvcName: "imagebuilder-storage"
    
    # For S3 storage:
    s3Config:
      endpoint: "s3.amazonaws.com"
      bucket: "flightctl-images"
      region: "us-east-1"
      accessKey: "AKIAIOSFODNN7EXAMPLE"
      secretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
```

## Kubernetes Integration

### Required Permissions
The imagebuilder service requires Kubernetes RBAC permissions:
- Create/Delete Jobs in the build namespace
- Create/Delete ConfigMaps for Containerfiles
- Create/Delete Secrets for registry credentials
- Create/Get PersistentVolumeClaims
- Watch Jobs for completion status

### Security Considerations
- Build Jobs run with privileged containers (required for buildah and bootc-image-builder)
- Registry credentials stored as Kubernetes Secrets
- Certificate data handled securely via API

## Deployment

### Build the Service
```bash
make build-imagebuilder
```

### Build Container Image
```bash
make flightctl-imagebuilder-container
```

### Run the Service
```bash
./bin/flightctl-imagebuilder
```

Or deploy to Kubernetes:
```bash
kubectl apply -f deploy/imagebuilder/deployment.yaml
```

## Usage

### Create an ImageBuild

```bash
curl -X POST http://localhost:7443/api/v1/imagebuilds \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "v1alpha1",
    "kind": "ImageBuild",
    "metadata": {
      "name": "my-edge-image"
    },
    "spec": {
      "baseImage": "quay.io/centos-bootc/centos-bootc:stream9",
      "customizations": {
        "packages": ["tmux", "vim", "git"],
        "users": [{
          "name": "admin",
          "groups": ["wheel"],
          "sshKeys": ["ssh-rsa AAAA..."]
        }]
      },
      "flightctlConfig": {
        "enrollmentService": {
          "service": {
            "server": "https://api.flightctl.example.com:7443"
          }
        }
      },
      "bootcExports": [{
        "type": "qcow2",
        "architecture": "x86_64"
      }],
      "containerRegistry": {
        "url": "quay.io/myorg",
        "username": "myuser",
        "password": "mypassword"
      },
      "pushToRegistry": true
    }
  }'
```

### Check Build Status

```bash
curl http://localhost:7443/api/v1/imagebuilds/my-edge-image
```

Response includes:
```json
{
  "status": {
    "phase": "Completed",
    "message": "Image build completed successfully",
    "containerImageRef": "quay.io/myorg/my-edge-image:20250104-143022",
    "bootcImageRefs": [{
      "type": "qcow2",
      "architecture": "x86_64",
      "storageRef": "/mnt/pvc/imagebuilder-storage/my-edge-image/qcow2.qcow2"
    }],
    "startTime": "2025-01-04T14:30:22Z",
    "completionTime": "2025-01-04T14:45:18Z"
  }
}
```

### Retry a Failed Build

Add a retry annotation:
```bash
curl -X PATCH http://localhost:7443/api/v1/imagebuilds/my-edge-image \
  -H "Content-Type: application/json" \
  -d '{
    "metadata": {
      "annotations": {
        "imagebuilder.flightctl.io/retry": "true"
      }
    }
  }'
```

### Cancel a Running Build

Add a cancellation annotation:
```bash
curl -X PATCH http://localhost:7443/api/v1/imagebuilds/my-edge-image \
  -H "Content-Type: application/json" \
  -d '{
    "metadata": {
      "annotations": {
        "imagebuilder.flightctl.io/cancel": "true"
      }
    }
  }'
```

### Get Build Logs

Retrieve logs from the running or completed build job:
```bash
curl http://localhost:7443/api/v1/imagebuilds/my-edge-image/logs
```

### Download Build Artifacts

Download generated disk images:
```bash
# Download QCOW2 image
curl -O http://localhost:7443/api/v1/imagebuilds/my-edge-image/downloads/qcow2.qcow2

# Download ISO image
curl -O http://localhost:7443/api/v1/imagebuilds/my-edge-image/downloads/iso.iso
```

### Generate Containerfile

Preview the Containerfile without creating a build:
```bash
curl -X POST http://localhost:7443/api/v1/imagebuilds/generate-containerfile \
  -H "Content-Type: application/json" \
  -d '{
    "baseImage": "quay.io/centos-bootc/centos-bootc:stream9",
    "customizations": {
      "packages": ["vim", "tmux"]
    }
  }'
```

## Development

### Running Locally
1. Ensure PostgreSQL is running
2. Run database migrations: `make db-migrate`
3. Start the API server: `./bin/flightctl-api`
4. Start the imagebuilder: `./bin/flightctl-imagebuilder`

### Testing
```bash
# Unit tests
make unit-test

# Integration tests
make integration-test
```

## Troubleshooting

### Build Job Failed
Check job logs via API:
```bash
# Via FlightCtl API
curl http://localhost:7443/api/v1/imagebuilds/my-edge-image/logs

# Or directly via kubectl
kubectl logs -n flightctl-builds job/build-my-edge-image
```

### Certificate Request Timeout
- Ensure the FlightCtl API is accessible
- Check CSR approval settings
- Verify enrollment signer is configured

### Bootc Image Build Failed
- Verify privileged containers are allowed in the namespace
- Check PVC storage availability
- Ensure sufficient resources (CPU, memory)

### Storage Upload Failed
- For S3: Verify credentials and bucket permissions
- For PVC: Check PVC is bound and has sufficient space
- For local: Ensure directory exists and is writable

## Performance Considerations

### Polling Interval
The BuildManager polls for pending builds every **10 seconds**. This provides a good balance between:
- Quick response to new build requests
- Minimal database load
- Efficient resource utilization

### Distributed Locking
The CleanupManager uses Kubernetes ConfigMaps for distributed locking to ensure:
- Only one instance cleans up orphaned resources at a time
- No race conditions when multiple replicas are running
- Lock expiration after 5 minutes to handle crashed instances

### Build Job Lifecycle
- Build Jobs are automatically cleaned up after completion
- Orphaned resources are cleaned up on service restart
- Recently cancelled builds are tracked in memory to avoid duplicate cancellation attempts

## Future Enhancements

Potential improvements for future versions:
- **Queue-based build system** (Redis/NATS) instead of polling for better scalability
- **Build caching** for faster subsequent builds using layer caching
- **Multi-architecture builds** in parallel (e.g., x86_64 and aarch64 simultaneously) NOTE: I did not test the aarch64!
- **Build artifact retention policies** with automatic cleanup of old images
- **Webhook notifications** on build completion, failure, or cancellation
- **Build templates and presets** for common image configurations
- **Integration with image scanning tools** (Trivy, Clair) for security scanning
- **Configurable polling intervals** via configuration or environment variables
- **Build prioritization** and queueing for resource management
- **Incremental builds** to speed up iterative development

