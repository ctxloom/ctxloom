# MLCM Fragment Server - GCP Deployment

Terraform configuration for deploying the MLCM Fragment Server to Google Cloud Platform.

## Architecture

- **Cloud Run**: Serverless container hosting for the gRPC/REST server
- **Firestore**: NoSQL database for fragment and persona storage
- **Artifact Registry**: Container image storage

## Prerequisites

1. [Terraform](https://www.terraform.io/downloads) >= 1.0
2. [Google Cloud SDK](https://cloud.google.com/sdk/docs/install)
3. A GCP project with billing enabled
4. Docker (for building container images)

## Quick Start

### 1. Authenticate with GCP

```bash
gcloud auth login
gcloud auth application-default login
```

### 2. Configure Variables

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your project details
```

### 3. Initialize Terraform

```bash
terraform init
```

### 4. Build and Push Container Image

```bash
# Configure Docker for Artifact Registry
gcloud auth configure-docker us-central1-docker.pkg.dev

# Build and push (from project root)
cd ..
docker build -t us-central1-docker.pkg.dev/YOUR_PROJECT/mlcm/mlcm-fragment-server:latest -f server/Dockerfile .
docker push us-central1-docker.pkg.dev/YOUR_PROJECT/mlcm/mlcm-fragment-server:latest
```

### 5. Deploy Infrastructure

```bash
cd terraform
terraform plan
terraform apply
```

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `project_id` | GCP project ID | (required) |
| `region` | GCP region | `us-central1` |
| `environment` | Environment name | `dev` |
| `min_instances` | Minimum Cloud Run instances | `0` |
| `max_instances` | Maximum Cloud Run instances | `10` |
| `cpu` | CPU allocation | `1` |
| `memory` | Memory allocation | `512Mi` |
| `enable_rate_limiting` | Enable rate limiting | `true` |
| `allow_unauthenticated` | Allow public access | `true` |

## Outputs

After deployment, Terraform outputs:

- `service_url`: The URL of the deployed service
- `artifact_registry_url`: URL for pushing container images
- `docker_build_command`: Command to build the container
- `docker_push_command`: Command to push the container

## Updating the Service

```bash
# Build new image
docker build -t $(terraform output -raw artifact_registry_url)/mlcm-fragment-server:latest -f server/Dockerfile .

# Push to registry
docker push $(terraform output -raw artifact_registry_url)/mlcm-fragment-server:latest

# Deploy new revision
gcloud run services update mlcm-fragment-server --region us-central1 --image $(terraform output -raw container_image)
```

## Cleanup

```bash
terraform destroy
```

## Cost Considerations

- **Cloud Run**: Pay per request, scales to zero when idle
- **Firestore**: Pay for storage and operations
- **Artifact Registry**: Pay for storage

For development, costs are minimal with `min_instances = 0`.
