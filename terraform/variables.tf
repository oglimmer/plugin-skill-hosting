# ---------------------------------------------------------------------------
# Identity / region
# ---------------------------------------------------------------------------

variable "region" {
  description = "AWS region for all regional resources (ECS, RDS, ALB, S3, ...)."
  type        = string
  default     = "eu-central-1"
}

variable "project" {
  description = "Project slug, used as a prefix for all resource names."
  type        = string
  default     = "plugin-skill-hosting"
}

variable "environment" {
  description = "Deployment environment label (dev, staging, prod, ...)."
  type        = string
  default     = "prod"
}

variable "tags" {
  description = "Additional tags merged into the default tag set on every taggable resource."
  type        = map(string)
  default     = {}
}

# ---------------------------------------------------------------------------
# Networking
# ---------------------------------------------------------------------------

variable "create_vpc" {
  description = "If true, create a new VPC with public + private subnets. If false, supply existing_vpc_id and existing_*_subnet_ids."
  type        = bool
  default     = true
}

variable "vpc_cidr" {
  description = "IPv4 CIDR for the new VPC. Only used when create_vpc = true."
  type        = string
  default     = "10.40.0.0/16"
}

variable "az_count" {
  description = "Number of availability zones to span. Minimum 2 for ALB + RDS HA."
  type        = number
  default     = 2
  validation {
    condition     = var.az_count >= 2 && var.az_count <= 3
    error_message = "az_count must be 2 or 3."
  }
}

variable "single_nat_gateway" {
  description = "Run one NAT gateway shared across all private subnets (cheaper) vs one per AZ (more resilient)."
  type        = bool
  default     = true
}

variable "existing_vpc_id" {
  description = "Existing VPC ID. Only used when create_vpc = false."
  type        = string
  default     = null
}

variable "existing_public_subnet_ids" {
  description = "Existing public subnet IDs (one per AZ). Only used when create_vpc = false."
  type        = list(string)
  default     = []
}

variable "existing_private_subnet_ids" {
  description = "Existing private subnet IDs (one per AZ). Only used when create_vpc = false."
  type        = list(string)
  default     = []
}

# ---------------------------------------------------------------------------
# Database (RDS for PostgreSQL 16)
# ---------------------------------------------------------------------------

variable "db_name" {
  description = "Postgres database name."
  type        = string
  default     = "marketplace"
}

variable "db_username" {
  description = "Postgres master username."
  type        = string
  default     = "marketplace"
}

variable "db_instance_class" {
  description = "RDS instance class. db.t4g.micro is fine for dev; bump for prod."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_allocated_storage" {
  description = "Initial storage in GiB."
  type        = number
  default     = 20
}

variable "db_max_allocated_storage" {
  description = "Storage autoscaling ceiling in GiB."
  type        = number
  default     = 100
}

variable "db_multi_az" {
  description = "Enable Multi-AZ standby for the RDS instance."
  type        = bool
  default     = false
}

variable "db_backup_retention_days" {
  description = "Days to retain automated RDS snapshots."
  type        = number
  default     = 7
}

variable "db_deletion_protection" {
  description = "Block accidental RDS deletion. Disable temporarily before tearing the stack down."
  type        = bool
  default     = true
}

variable "s3_force_destroy" {
  description = "Allow `terraform destroy` to wipe the frontend + logs buckets even if they contain objects or noncurrent versions. Flip to true before tear-down."
  type        = bool
  default     = false
}

# ---------------------------------------------------------------------------
# Backend (ECS Fargate)
# ---------------------------------------------------------------------------

variable "backend_image" {
  description = "Container image for the backend service."
  type        = string
  default     = "ghcr.io/oglimmer/plugin-skill-hosting-backend:latest"
}

variable "backend_cpu" {
  description = "Fargate task CPU units (256 / 512 / 1024 / 2048 / 4096)."
  type        = number
  default     = 512
}

variable "backend_memory" {
  description = "Fargate task memory in MB. Must be valid for the chosen CPU."
  type        = number
  default     = 1024
}

variable "backend_ephemeral_storage_gib" {
  description = "Fargate ephemeral storage for /data git repos (21-200 GiB). Rebuilt from Postgres on every cold start."
  type        = number
  default     = 30
  validation {
    condition     = var.backend_ephemeral_storage_gib >= 21 && var.backend_ephemeral_storage_gib <= 200
    error_message = "Fargate ephemeral storage must be between 21 and 200 GiB."
  }
}

variable "backend_desired_count" {
  description = "Steady-state Fargate task count. Keep at 1 — git repos live on per-task ephemeral storage."
  type        = number
  default     = 1
}

variable "backend_max_count" {
  description = "Auto-scaling ceiling. Set equal to desired_count to disable scaling."
  type        = number
  default     = 1
}

variable "backend_cpu_architecture" {
  description = "Fargate CPU architecture. ARM64 (Graviton) is ~20% cheaper if the image supports it."
  type        = string
  default     = "ARM64"
  validation {
    condition     = contains(["ARM64", "X86_64"], var.backend_cpu_architecture)
    error_message = "backend_cpu_architecture must be ARM64 or X86_64."
  }
}

variable "backend_health_check_grace_period_seconds" {
  description = "Seconds the ALB tolerates an unhealthy task on first launch — must cover full rematerialization time."
  type        = number
  default     = 300
}

# ---------------------------------------------------------------------------
# Application configuration (non-secret)
# ---------------------------------------------------------------------------

variable "marketplace_name" {
  description = "Name embedded in marketplace.json and used as the owner name."
  type        = string
  default     = "oglimmer-marketplace"
}

variable "default_license" {
  description = "Default license prefilled in the 'new plugin' form."
  type        = string
  default     = "MIT"
}

variable "auth_mode" {
  description = "Authentication mode: 'password' (built-in email/password + JWT) or 'oidc'."
  type        = string
  default     = "password"
  validation {
    condition     = contains(["password", "oidc"], var.auth_mode)
    error_message = "auth_mode must be 'password' or 'oidc'."
  }
}

variable "oidc_issuer_url" {
  description = "OIDC issuer URL. Required when auth_mode = oidc."
  type        = string
  default     = ""

  validation {
    condition     = var.auth_mode != "oidc" || length(var.oidc_issuer_url) > 0
    error_message = "oidc_issuer_url is required when auth_mode = \"oidc\"."
  }
}

variable "oidc_client_id" {
  description = "OIDC client ID. Required when auth_mode = oidc."
  type        = string
  default     = ""

  validation {
    condition     = var.auth_mode != "oidc" || length(var.oidc_client_id) > 0
    error_message = "oidc_client_id is required when auth_mode = \"oidc\"."
  }
}

variable "oidc_redirect_url" {
  description = "OIDC redirect URL. Leave empty to derive from PUBLIC_BASE_URL."
  type        = string
  default     = ""
}

variable "oidc_scopes" {
  description = "OIDC scope string."
  type        = string
  default     = "openid email profile"
}

variable "anthropic_model" {
  description = "Anthropic model id used for generation."
  type        = string
  default     = "claude-sonnet-4-6"
}

# ---------------------------------------------------------------------------
# Secrets (seeded into AWS Secrets Manager on first apply)
# ---------------------------------------------------------------------------

variable "jwt_secret" {
  description = "JWT signing secret. Must be at least 32 characters."
  type        = string
  sensitive   = true
  validation {
    condition     = length(var.jwt_secret) >= 32
    error_message = "jwt_secret must be at least 32 characters."
  }
}

variable "anthropic_api_key" {
  description = "Anthropic API key. Leave empty to disable generation features."
  type        = string
  sensitive   = true
  default     = ""
}

variable "oidc_client_secret" {
  description = "OIDC client secret. Required when auth_mode = oidc."
  type        = string
  sensitive   = true
  default     = ""

  validation {
    condition     = var.auth_mode != "oidc" || length(var.oidc_client_secret) > 0
    error_message = "oidc_client_secret is required when auth_mode = \"oidc\"."
  }
}

# ---------------------------------------------------------------------------
# CloudFront / edge
# ---------------------------------------------------------------------------

variable "cloudfront_price_class" {
  description = "CloudFront price class. PriceClass_100 = US/EU only (cheapest)."
  type        = string
  default     = "PriceClass_100"
  validation {
    condition     = contains(["PriceClass_All", "PriceClass_200", "PriceClass_100"], var.cloudfront_price_class)
    error_message = "Must be PriceClass_All, PriceClass_200, or PriceClass_100."
  }
}

variable "enable_waf" {
  description = "Attach AWS-managed WAFv2 web ACL to the CloudFront distribution."
  type        = bool
  default     = true
}

# ---------------------------------------------------------------------------
# Observability
# ---------------------------------------------------------------------------

variable "log_retention_days" {
  description = "CloudWatch log group retention."
  type        = number
  default     = 30
}

variable "alarm_email" {
  description = "Email subscribed to the alarms SNS topic. Leave empty to skip notifications (alarms still fire in CloudWatch)."
  type        = string
  default     = ""

  validation {
    condition     = var.alarm_email == "" || can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.alarm_email))
    error_message = "alarm_email must be empty or a valid email address."
  }
}
