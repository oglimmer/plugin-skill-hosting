locals {
  # CloudFront routes these paths to the ALB origin (the rest of the world
  # comes from S3). Write methods are enabled for /api, /git, /mcp because
  # the backend accepts POST/PUT/DELETE on those; /marketplace.json and the
  # probe endpoints are read-only. Compression is off for git smart-HTTP
  # (already framed/packfiled) and for MCP SSE (must flush immediately).
  backend_behaviors = [
    { pattern = "/api/*", compress = true, methods = ["GET", "HEAD", "OPTIONS", "PUT", "PATCH", "POST", "DELETE"] },
    { pattern = "/git/*", compress = false, methods = ["GET", "HEAD", "OPTIONS", "PUT", "PATCH", "POST", "DELETE"] },
    { pattern = "/mcp*", compress = false, methods = ["GET", "HEAD", "OPTIONS", "PUT", "PATCH", "POST", "DELETE"] },
    { pattern = "/marketplace.json", compress = true, methods = ["GET", "HEAD"] },
    { pattern = "/healthz", compress = false, methods = ["GET", "HEAD"] },
    { pattern = "/readyz", compress = false, methods = ["GET", "HEAD"] },
  ]
}

resource "aws_cloudfront_origin_access_control" "s3" {
  name                              = "${local.name}-s3-oac"
  description                       = "OAC for the SPA bucket"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

data "aws_cloudfront_cache_policy" "optimized" {
  name = "Managed-CachingOptimized"
}

data "aws_cloudfront_cache_policy" "disabled" {
  name = "Managed-CachingDisabled"
}

# Forwards all viewer headers/cookies/query strings to the origin except
# the Host header (which CloudFront must rewrite to the ALB DNS name).
data "aws_cloudfront_origin_request_policy" "all_viewer_except_host" {
  name = "Managed-AllViewerExceptHostHeader"
}

data "aws_cloudfront_response_headers_policy" "security_headers" {
  name = "Managed-SecurityHeadersPolicy"
}

resource "aws_cloudfront_distribution" "this" {
  enabled             = true
  comment             = local.name
  default_root_object = "index.html"
  price_class         = var.cloudfront_price_class
  http_version        = "http2and3"
  is_ipv6_enabled     = true

  web_acl_id = var.enable_waf ? aws_wafv2_web_acl.this[0].arn : null

  # -------- Origins --------

  origin {
    origin_id                = "s3-frontend"
    domain_name              = aws_s3_bucket.frontend.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.s3.id
  }

  origin {
    origin_id   = "alb-backend"
    domain_name = aws_lb.this.dns_name

    custom_origin_config {
      http_port                = 80
      https_port               = 443
      origin_protocol_policy   = "http-only"
      origin_ssl_protocols     = ["TLSv1.2"]
      origin_keepalive_timeout = 60
      # MCP /mcp keeps an SSE stream open; bump origin read timeout so events
      # flow without being reaped. Hard max is 60s without a service quota
      # increase — request one if longer streams are needed.
      origin_read_timeout = 60
    }

    custom_header {
      name  = "X-CloudFront-Secret"
      value = random_password.cf_origin_secret.result
    }
  }

  # -------- Default behavior: serve SPA from S3 --------

  default_cache_behavior {
    target_origin_id           = "s3-frontend"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.optimized.id
    response_headers_policy_id = data.aws_cloudfront_response_headers_policy.security_headers.id
  }

  # -------- Backend behaviors (all routed to the ALB origin) --------
  #
  # Order matters: CloudFront evaluates ordered_cache_behavior blocks in the
  # order Terraform writes them, which mirrors local.backend_behaviors below.

  dynamic "ordered_cache_behavior" {
    for_each = local.backend_behaviors
    content {
      path_pattern             = ordered_cache_behavior.value.pattern
      target_origin_id         = "alb-backend"
      viewer_protocol_policy   = "redirect-to-https"
      allowed_methods          = ordered_cache_behavior.value.methods
      cached_methods           = ["GET", "HEAD"]
      compress                 = ordered_cache_behavior.value.compress
      cache_policy_id          = data.aws_cloudfront_cache_policy.disabled.id
      origin_request_policy_id = data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id
    }
  }

  # SPA client-side routing — serve index.html for any 403/404 from S3.
  custom_error_response {
    error_code            = 403
    response_code         = 200
    response_page_path    = "/index.html"
    error_caching_min_ttl = 0
  }

  custom_error_response {
    error_code            = 404
    response_code         = 200
    response_page_path    = "/index.html"
    error_caching_min_ttl = 0
  }

  viewer_certificate {
    # When using the default *.cloudfront.net cert, AWS forces the minimum
    # protocol to TLSv1 and ignores any override here. Setting it would
    # cause perpetual diff. Switch to a custom ACM cert + alias to raise
    # the floor.
    cloudfront_default_certificate = true
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  logging_config {
    bucket          = aws_s3_bucket.logs.bucket_domain_name
    prefix          = "cloudfront/"
    include_cookies = false
  }

  depends_on = [aws_s3_bucket_policy.logs]
}

# Allow CloudFront (and only this distribution) to read from the SPA bucket.
resource "aws_s3_bucket_policy" "frontend" {
  bucket = aws_s3_bucket.frontend.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "AllowCloudFrontReadViaOAC"
      Effect    = "Allow"
      Principal = { Service = "cloudfront.amazonaws.com" }
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.frontend.arn}/*"
      Condition = {
        StringEquals = {
          "AWS:SourceArn" = aws_cloudfront_distribution.this.arn
        }
      }
    }]
  })
}
