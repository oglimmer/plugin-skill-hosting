# Customer-managed KMS key for all secrets (Well-Architected: Security).
resource "aws_kms_key" "secrets" {
  description             = "${local.name} Secrets Manager encryption"
  enable_key_rotation     = true
  deletion_window_in_days = 7
}

resource "aws_kms_alias" "secrets" {
  name          = "alias/${local.name}-secrets"
  target_key_id = aws_kms_key.secrets.id
}

# Randomly-generated DB password — fed straight into RDS and into the runtime
# secret below. No special characters so the URL-encoding stays trivial.
resource "random_password" "db" {
  length  = 32
  special = false
}

# Random shared secret used by CloudFront -> ALB origin requests. The ALB
# listener rule rejects any request that doesn't carry it, preventing direct
# public access to the ALB.
resource "random_password" "cf_origin_secret" {
  length  = 48
  special = false
}

# Single Secrets Manager entry holding every runtime secret the backend needs.
# ECS pulls individual JSON keys via the `secrets[*].valueFrom` references in
# the task definition (see ecs.tf).
resource "aws_secretsmanager_secret" "app" {
  name                    = "${local.name}/runtime"
  description             = "Runtime secrets (DATABASE_URL, JWT, OIDC, Anthropic) for ${local.name}"
  kms_key_id              = aws_kms_key.secrets.arn
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "app" {
  secret_id = aws_secretsmanager_secret.app.id
  secret_string = jsonencode({
    DATABASE_URL       = "postgres://${var.db_username}:${random_password.db.result}@${aws_db_instance.this.address}:5432/${var.db_name}?sslmode=require"
    JWT_SECRET         = var.jwt_secret
    ANTHROPIC_API_KEY  = var.anthropic_api_key
    OIDC_CLIENT_SECRET = var.oidc_client_secret
  })
}
