output "cloudfront_domain" {
  description = "Public HTTPS hostname for the application."
  value       = aws_cloudfront_distribution.this.domain_name
}

output "public_base_url" {
  description = "PUBLIC_BASE_URL the backend embeds in marketplace.json and OIDC URLs."
  value       = local.public_base_url
}

output "cloudfront_distribution_id" {
  description = "CloudFront distribution ID — used by the SPA deploy command for invalidations."
  value       = aws_cloudfront_distribution.this.id
}

output "frontend_bucket" {
  description = "S3 bucket holding the built SPA assets."
  value       = aws_s3_bucket.frontend.id
}

output "alb_dns_name" {
  description = "Internal-only ALB DNS name (CloudFront origin). Direct hits return 403."
  value       = aws_lb.this.dns_name
}

output "ecs_cluster_name" {
  value = aws_ecs_cluster.this.name
}

output "ecs_service_name" {
  value = aws_ecs_service.backend.name
}

output "rds_endpoint" {
  description = "Postgres endpoint — only reachable from ECS tasks."
  value       = aws_db_instance.this.address
}

output "app_secret_arn" {
  description = "Secrets Manager ARN holding DATABASE_URL / JWT_SECRET / ANTHROPIC_API_KEY / OIDC_CLIENT_SECRET."
  value       = aws_secretsmanager_secret.app.arn
}

output "vpc_id" {
  description = "VPC id (created or supplied)."
  value       = local.vpc_id
}

output "deploy_spa_commands" {
  description = "Run after `terraform apply` to publish the built SPA to S3 and invalidate CloudFront."
  value       = <<-EOT
    # 1) Build the SPA
    cd frontend && npm ci && npm run build

    # 2) Upload static assets (long cache) and index.html (no cache)
    aws s3 sync dist/ s3://${aws_s3_bucket.frontend.id}/ \
      --delete \
      --cache-control "public, max-age=31536000, immutable" \
      --exclude "index.html"

    aws s3 cp dist/index.html s3://${aws_s3_bucket.frontend.id}/index.html \
      --cache-control "no-cache, no-store, must-revalidate"

    # 3) Invalidate the SPA entrypoint
    aws cloudfront create-invalidation \
      --distribution-id ${aws_cloudfront_distribution.this.id} \
      --paths "/index.html" "/"
  EOT
}
