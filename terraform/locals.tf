data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_region" "current" {}

data "aws_caller_identity" "current" {}

locals {
  name = "${var.project}-${var.environment}"

  tags = merge({
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
  }, var.tags)

  azs = slice(data.aws_availability_zones.available.names, 0, var.az_count)

  vpc_id             = var.create_vpc ? aws_vpc.this[0].id : var.existing_vpc_id
  public_subnet_ids  = var.create_vpc ? aws_subnet.public[*].id : var.existing_public_subnet_ids
  private_subnet_ids = var.create_vpc ? aws_subnet.private[*].id : var.existing_private_subnet_ids

  # PUBLIC_BASE_URL is the CloudFront default hostname (HTTPS). Computed in
  # ecs.tf to break the reading order cleanly — see local.public_base_url.
}
