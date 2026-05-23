resource "aws_cloudwatch_log_group" "backend" {
  name              = "/aws/ecs/${local.name}/backend"
  retention_in_days = var.log_retention_days
}
