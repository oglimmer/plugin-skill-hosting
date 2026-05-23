# Optionally create a fresh VPC with one public and one private subnet per AZ,
# a shared (or per-AZ) NAT gateway, and VPC flow logs. Disabled when
# create_vpc = false — in that case the operator supplies existing subnet IDs.

resource "aws_vpc" "this" {
  count                = var.create_vpc ? 1 : 0
  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = {
    Name = "${local.name}-vpc"
  }
}

resource "aws_internet_gateway" "this" {
  count  = var.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id

  tags = {
    Name = "${local.name}-igw"
  }
}

# Public subnets — host the ALB and NAT gateway(s).
resource "aws_subnet" "public" {
  count                   = var.create_vpc ? var.az_count : 0
  vpc_id                  = aws_vpc.this[0].id
  cidr_block              = cidrsubnet(var.vpc_cidr, 4, count.index)
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = false

  tags = {
    Name = "${local.name}-public-${local.azs[count.index]}"
    Tier = "public"
  }
}

# Private subnets — host the ECS tasks and the RDS instance.
resource "aws_subnet" "private" {
  count             = var.create_vpc ? var.az_count : 0
  vpc_id            = aws_vpc.this[0].id
  cidr_block        = cidrsubnet(var.vpc_cidr, 4, count.index + 8)
  availability_zone = local.azs[count.index]

  tags = {
    Name = "${local.name}-private-${local.azs[count.index]}"
    Tier = "private"
  }
}

# ----- NAT gateway(s) -----

resource "aws_eip" "nat" {
  count  = var.create_vpc ? (var.single_nat_gateway ? 1 : var.az_count) : 0
  domain = "vpc"

  tags = {
    Name = "${local.name}-nat-${count.index}"
  }
}

resource "aws_nat_gateway" "this" {
  count         = var.create_vpc ? (var.single_nat_gateway ? 1 : var.az_count) : 0
  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id

  tags = {
    Name = "${local.name}-nat-${count.index}"
  }

  depends_on = [aws_internet_gateway.this]
}

# ----- Route tables -----

resource "aws_route_table" "public" {
  count  = var.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id

  tags = {
    Name = "${local.name}-public"
  }
}

resource "aws_route" "public_default" {
  count                  = var.create_vpc ? 1 : 0
  route_table_id         = aws_route_table.public[0].id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this[0].id
}

resource "aws_route_table_association" "public" {
  count          = var.create_vpc ? var.az_count : 0
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

resource "aws_route_table" "private" {
  count  = var.create_vpc ? var.az_count : 0
  vpc_id = aws_vpc.this[0].id

  tags = {
    Name = "${local.name}-private-${local.azs[count.index]}"
  }
}

resource "aws_route" "private_default" {
  count                  = var.create_vpc ? var.az_count : 0
  route_table_id         = aws_route_table.private[count.index].id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = var.single_nat_gateway ? aws_nat_gateway.this[0].id : aws_nat_gateway.this[count.index].id
}

resource "aws_route_table_association" "private" {
  count          = var.create_vpc ? var.az_count : 0
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[count.index].id
}

# ----- VPC Flow Logs (Well-Architected: Security pillar) -----

resource "aws_cloudwatch_log_group" "vpc_flow" {
  count             = var.create_vpc ? 1 : 0
  name              = "/aws/vpc/${local.name}/flow"
  retention_in_days = var.log_retention_days
}

resource "aws_iam_role" "vpc_flow" {
  count = var.create_vpc ? 1 : 0
  name  = "${local.name}-vpc-flow-logs"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "vpc-flow-logs.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "vpc_flow" {
  count = var.create_vpc ? 1 : 0
  role  = aws_iam_role.vpc_flow[0].id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "logs:CreateLogStream",
        "logs:PutLogEvents",
        "logs:DescribeLogStreams",
        "logs:DescribeLogGroups"
      ]
      Resource = "${aws_cloudwatch_log_group.vpc_flow[0].arn}:*"
    }]
  })
}

resource "aws_flow_log" "this" {
  count           = var.create_vpc ? 1 : 0
  vpc_id          = aws_vpc.this[0].id
  log_destination = aws_cloudwatch_log_group.vpc_flow[0].arn
  iam_role_arn    = aws_iam_role.vpc_flow[0].arn
  traffic_type    = "ALL"
}
