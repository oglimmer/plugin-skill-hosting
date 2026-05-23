resource "aws_ecs_cluster" "this" {
  name = "${local.name}-cluster"

  # "enhanced" emits per-task observability metrics (CPU/memory/network/disk
  # plus aggregated container metrics) and feeds the Container Insights
  # console without an extra agent.
  setting {
    name  = "containerInsights"
    value = "enhanced"
  }
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
    base              = 1
  }
}

resource "aws_security_group" "ecs" {
  name        = "${local.name}-ecs"
  description = "ECS backend tasks - ingress from ALB only"
  vpc_id      = local.vpc_id

  tags = {
    Name = "${local.name}-ecs"
  }
}

resource "aws_vpc_security_group_ingress_rule" "ecs_from_alb" {
  security_group_id            = aws_security_group.ecs.id
  referenced_security_group_id = aws_security_group.alb.id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
  description                  = "Backend port from ALB"
}

resource "aws_vpc_security_group_egress_rule" "ecs_egress_all" {
  security_group_id = aws_security_group.ecs.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
  description       = "Egress for RDS, Anthropic API, OIDC, ECR, Secrets Manager"
}

# Base URL the backend embeds in marketplace.json and OIDC redirects.
# CloudFront's default cert covers *.cloudfront.net so HTTPS works without a
# custom domain.
locals {
  public_base_url   = "https://${aws_cloudfront_distribution.this.domain_name}"
  oidc_redirect_url = var.oidc_redirect_url != "" ? var.oidc_redirect_url : "${local.public_base_url}/api/auth/oidc/callback"
}

resource "aws_ecs_task_definition" "backend" {
  family                   = "${local.name}-backend"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = var.backend_cpu
  memory                   = var.backend_memory
  execution_role_arn       = aws_iam_role.ecs_task_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  ephemeral_storage {
    size_in_gib = var.backend_ephemeral_storage_gib
  }

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.backend_cpu_architecture
  }

  container_definitions = jsonencode([{
    name      = "backend"
    image     = var.backend_image
    essential = true

    portMappings = [{
      containerPort = 8080
      protocol      = "tcp"
    }]

    environment = [
      { name = "LISTEN_ADDR", value = ":8080" },
      { name = "DATA_DIR", value = "/data" },
      { name = "PUBLIC_BASE_URL", value = local.public_base_url },
      { name = "MARKETPLACE_NAME", value = var.marketplace_name },
      { name = "DEFAULT_LICENSE", value = var.default_license },
      { name = "AUTH_MODE", value = var.auth_mode },
      { name = "OIDC_ISSUER_URL", value = var.oidc_issuer_url },
      { name = "OIDC_CLIENT_ID", value = var.oidc_client_id },
      { name = "OIDC_REDIRECT_URL", value = local.oidc_redirect_url },
      { name = "OIDC_SCOPES", value = var.oidc_scopes },
      { name = "ANTHROPIC_MODEL", value = var.anthropic_model },
      # Rebuild git repos from Postgres on every cold start — /data is on
      # ephemeral Fargate storage and doesn't survive task replacement.
      { name = "REMATERIALIZE_ON_STARTUP", value = "true" },
    ]

    secrets = [
      { name = "DATABASE_URL", valueFrom = "${aws_secretsmanager_secret.app.arn}:DATABASE_URL::" },
      { name = "JWT_SECRET", valueFrom = "${aws_secretsmanager_secret.app.arn}:JWT_SECRET::" },
      { name = "ANTHROPIC_API_KEY", valueFrom = "${aws_secretsmanager_secret.app.arn}:ANTHROPIC_API_KEY::" },
      { name = "OIDC_CLIENT_SECRET", valueFrom = "${aws_secretsmanager_secret.app.arn}:OIDC_CLIENT_SECRET::" },
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.backend.name
        awslogs-region        = data.aws_region.current.region
        awslogs-stream-prefix = "backend"
      }
    }
  }])
}

resource "aws_ecs_service" "backend" {
  name             = "${local.name}-backend"
  cluster          = aws_ecs_cluster.this.id
  task_definition  = aws_ecs_task_definition.backend.arn
  desired_count    = var.backend_desired_count
  launch_type      = "FARGATE"
  platform_version = "1.4.0"

  health_check_grace_period_seconds = var.backend_health_check_grace_period_seconds
  enable_execute_command            = true
  propagate_tags                    = "SERVICE"
  availability_zone_rebalancing     = "ENABLED"

  network_configuration {
    subnets          = local.private_subnet_ids
    security_groups  = [aws_security_group.ecs.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.backend.arn
    container_name   = "backend"
    container_port   = 8080
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  deployment_controller {
    type = "ECS"
  }

  lifecycle {
    # desired_count is managed by app autoscaling once enabled.
    ignore_changes = [desired_count]
  }

  depends_on = [
    aws_lb_listener_rule.cf_only,
    aws_secretsmanager_secret_version.app,
  ]
}

# ----- Optional auto-scaling -----

resource "aws_appautoscaling_target" "backend" {
  count              = var.backend_max_count > var.backend_desired_count ? 1 : 0
  service_namespace  = "ecs"
  scalable_dimension = "ecs:service:DesiredCount"
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.backend.name}"
  min_capacity       = var.backend_desired_count
  max_capacity       = var.backend_max_count
}

resource "aws_appautoscaling_policy" "backend_cpu" {
  count              = var.backend_max_count > var.backend_desired_count ? 1 : 0
  name               = "${local.name}-backend-cpu"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.backend[0].service_namespace
  scalable_dimension = aws_appautoscaling_target.backend[0].scalable_dimension
  resource_id        = aws_appautoscaling_target.backend[0].resource_id

  target_tracking_scaling_policy_configuration {
    target_value = 60
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}
