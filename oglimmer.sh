#!/usr/bin/env bash

set -euo pipefail

# Define script metadata
SCRIPT_NAME=$(basename "$0")
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Default configuration
DEFAULT_REGISTRIES=("ghcr.io/oglimmer")
DEFAULT_FRONTEND_DEPLOYMENT="plugin-skill-hosting-frontend"
DEFAULT_BACKEND_DEPLOYMENT="plugin-skill-hosting-backend"

# Configuration variables (can be overridden by parameters)
REGISTRIES=("${DEFAULT_REGISTRIES[@]}")
FRONTEND_IMAGES=()
BACKEND_IMAGES=()
FRONTEND_DEPLOYMENT="$DEFAULT_FRONTEND_DEPLOYMENT"
BACKEND_DEPLOYMENT="$DEFAULT_BACKEND_DEPLOYMENT"

# Directories
BACKEND_DIR="$SCRIPT_DIR/backend"
FRONTEND_DIR="$SCRIPT_DIR/frontend"

# Default options (can be overridden by environment variables)
BUILD_FRONTEND="${BUILD_FRONTEND:-false}"
BUILD_BACKEND="${BUILD_BACKEND:-false}"
VERBOSE="${VERBOSE:-false}"
DRY_RUN="${DRY_RUN:-false}"
RESTART="${RESTART:-true}"
PUSH="${PUSH:-true}"
HELP=false
PLATFORM="${PLATFORM:-arm64}"
RELEASE_MODE=false
RELEASE_BUMP=""
SHOW_VERSIONS=false
DEV_COMMAND=""

# Color output (only if terminal supports it)
if [[ -t 1 ]] && command -v tput >/dev/null 2>&1; then
  BOLD="$(tput bold)"
  GREEN="$(tput setaf 2)"
  YELLOW="$(tput setaf 3)"
  RED="$(tput setaf 1)"
  BLUE="$(tput setaf 4)"
  RESET="$(tput sgr0)"
else
  BOLD="" GREEN="" YELLOW="" RED="" BLUE="" RESET=""
fi

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${RESET} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${RESET} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${RESET} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${RESET} $1" >&2
}

# Verbose logging
log_verbose() {
    if [[ "$VERBOSE" == true ]]; then
        echo -e "${BLUE}[VERBOSE]${RESET} $1"
    fi
}

# Execute command with dry-run and verbose support
execute_cmd() {
    local cmd="$1"

    if [[ "$DRY_RUN" == true ]]; then
        echo -e "${YELLOW}[DRY-RUN]${RESET} ${cmd}"
        return 0
    else
        log_verbose "Executing: $cmd"
        if [[ "$VERBOSE" == true ]]; then
            eval "$cmd"
        else
            eval "$cmd" >/dev/null 2>&1
        fi
    fi
}

# Show usage information
show_help() {
    cat << EOF
Usage: ${SCRIPT_NAME} [OPTIONS] [COMMAND]

Build, deploy, and release plugin-skill-hosting application components.

COMMANDS:
    build               Build and deploy components (default)
    release             Bump semver, commit, tag, then build and deploy
    helm-push           Package and push the Helm chart to ghcr.io/oglimmer
    show                Show current frontend version
    start               Build and run backend locally in background
    stop                Stop the local backend process
    status              Show whether the local backend is running
    logs                Tail the local backend log file
    test                Run backend tests

BUILD OPTIONS:
    -f, --frontend          Build and deploy frontend only
    -b, --backend           Build and deploy backend only
    -a, --all               Build and deploy both frontend and backend (default if no component specified)
    -v, --verbose           Enable verbose output
    -n, --no-restart        Skip Kubernetes deployment restart
    --no-push               Skip pushing images to registry
    --dry-run               Show what would be done without executing

RELEASE OPTIONS:
    --bump major|minor|bugfix  Skip the interactive prompt and bump that semver part

    # Registry configuration options
    --registries "REG1,REG2"    Comma-separated list of registries to push to (default: ${DEFAULT_REGISTRIES[0]})
    --frontend-deploy NAME      Frontend deployment name (default: $DEFAULT_FRONTEND_DEPLOYMENT)
    --backend-deploy NAME       Backend deployment name (default: $DEFAULT_BACKEND_DEPLOYMENT)

    # Platform options
    --platform PLATFORM        Target platform(s) for Docker build:
                               - amd64: Build for AMD64/x86_64 architecture
                               - arm64: Build for ARM64 architecture
                               - multi: Build for both amd64 and arm64 (multi-platform)
                               - auto: Detect current platform automatically

    -h, --help              Show this help message

EXAMPLES:
    ${SCRIPT_NAME} build                                    # Build and deploy both components with defaults
    ${SCRIPT_NAME} build -f                                 # Build and deploy frontend only
    ${SCRIPT_NAME} build -b -v                              # Build and deploy backend with verbose output
    ${SCRIPT_NAME} release                                  # Interactive semver bump, then build and deploy
    ${SCRIPT_NAME} release --bump minor                     # Non-interactive minor bump, then build and deploy
    ${SCRIPT_NAME} show                                     # Show current version
    ${SCRIPT_NAME} build --registries my-registry.com       # Use custom registry
    ${SCRIPT_NAME} build --platform amd64                   # Build for AMD64 only
    ${SCRIPT_NAME} start                                    # Build and run backend locally

ENVIRONMENT VARIABLES:
    FRONTEND_DEPLOYMENT     Override default frontend deployment name
    BACKEND_DEPLOYMENT      Override default backend deployment name
    PLATFORM                Override default platform (amd64|arm64|multi|auto)
    DEFAULT_REGISTRIES_ENV  Override default registries (comma-separated)
    VERBOSE                 Enable verbose mode (true/false)
    DRY_RUN                 Enable dry-run mode (true/false)
    PUSH                    Enable/disable pushing to registry (true/false)
    RESTART                 Enable/disable Kubernetes restart (true/false)

EOF
}

# Parse command line arguments
parse_args() {
    # Check if first argument is a command
    if [[ $# -gt 0 ]]; then
        case $1 in
            build)
                shift
                ;;
            release)
                RELEASE_MODE=true
                shift
                ;;
            show)
                SHOW_VERSIONS=true
                shift
                ;;
            start|stop|status|logs|test|helm-push)
                DEV_COMMAND="$1"
                shift
                return
                ;;
            help|-h|--help)
                HELP=true
                shift
                ;;
        esac
    fi

    while [[ $# -gt 0 ]]; do
        case $1 in
            -f|--frontend)
                BUILD_FRONTEND=true
                shift
                ;;
            -b|--backend)
                BUILD_BACKEND=true
                shift
                ;;
            -a|--all)
                BUILD_FRONTEND=true
                BUILD_BACKEND=true
                shift
                ;;
            -v|--verbose)
                VERBOSE=true
                shift
                ;;
            -n|--no-restart)
                RESTART=false
                shift
                ;;
            --no-push)
                PUSH=false
                shift
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            --registries)
                # Clear existing registries and parse comma-separated list
                REGISTRIES=()
                IFS=',' read -ra ADDR <<< "$2"
                for registry in "${ADDR[@]}"; do
                    REGISTRIES+=("$(echo "$registry" | xargs)")  # trim whitespace
                done
                shift 2
                ;;
            --frontend-deploy)
                FRONTEND_DEPLOYMENT="$2"
                shift 2
                ;;
            --backend-deploy)
                BACKEND_DEPLOYMENT="$2"
                shift 2
                ;;
            --platform)
                PLATFORM="$2"
                shift 2
                ;;
            --bump)
                RELEASE_BUMP="$2"
                shift 2
                ;;
            -h|--help)
                HELP=true
                shift
                ;;
            *)
                log_error "Unknown option: $1"
                show_help
                exit 1
                ;;
        esac
    done

    # Handle environment variable overrides
    FRONTEND_DEPLOYMENT="${FRONTEND_DEPLOYMENT:-$FRONTEND_DEPLOYMENT}"
    BACKEND_DEPLOYMENT="${BACKEND_DEPLOYMENT:-$BACKEND_DEPLOYMENT}"
    PLATFORM="${DOCKER_PLATFORM:-$PLATFORM}"

    # Override default registries from environment if set
    if [[ -n "${DEFAULT_REGISTRIES_ENV:-}" ]]; then
        REGISTRIES=()
        IFS=',' read -ra ADDR <<< "$DEFAULT_REGISTRIES_ENV"
        for registry in "${ADDR[@]}"; do
            REGISTRIES+=("$(echo "$registry" | xargs)")
        done
    fi

    # Build image arrays from registries
    if [[ ${#REGISTRIES[@]} -gt 0 ]]; then
        FRONTEND_IMAGES=()
        BACKEND_IMAGES=()
        for registry in "${REGISTRIES[@]}"; do
            FRONTEND_IMAGES+=("$registry/plugin-skill-hosting-frontend")
            BACKEND_IMAGES+=("$registry/plugin-skill-hosting-backend")
        done
    else
        # Fallback to defaults if no registries specified
        FRONTEND_IMAGES=("${DEFAULT_REGISTRIES[0]}/plugin-skill-hosting-frontend")
        BACKEND_IMAGES=("${DEFAULT_REGISTRIES[0]}/plugin-skill-hosting-backend")
    fi

    # Validate platform parameter
    if [[ -n "$PLATFORM" && ! "$PLATFORM" =~ ^(amd64|arm64|multi|auto)$ ]]; then
        log_error "Invalid platform: $PLATFORM. Must be one of: amd64, arm64, multi, auto"
        exit 1
    fi

    # Validate release bump parameter
    if [[ -n "$RELEASE_BUMP" && ! "$RELEASE_BUMP" =~ ^(major|minor|bugfix|patch)$ ]]; then
        log_error "Invalid --bump: $RELEASE_BUMP. Must be one of: major, minor, bugfix"
        exit 1
    fi

    # Validate conflicting options
    if [[ "$PUSH" == false && "$RESTART" == true && "$RELEASE_MODE" == false ]]; then
        log_warning "Cannot restart deployments without pushing images. Setting --no-restart."
        RESTART=false
    fi

    # If no component specified for build mode, build both
    if [[ "$RELEASE_MODE" == false && "$SHOW_VERSIONS" == false && "$BUILD_FRONTEND" == false && "$BUILD_BACKEND" == false ]]; then
        BUILD_FRONTEND=true
        BUILD_BACKEND=true
    fi
}

# Check if required tools are available
check_prerequisites() {
    local tools=("docker" "kubectl")

    # Add additional tools for release mode
    if [[ "$RELEASE_MODE" == true ]]; then
        tools+=("npm" "git")
    fi

    # Add tools needed for helm push
    if [[ "$RELEASE_MODE" == true || "$DEV_COMMAND" == "helm-push" ]]; then
        tools+=("helm" "gh")
    fi

    local missing_deps=()
    for tool in "${tools[@]}"; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            missing_deps+=("$tool")
        fi
    done

    if [[ ${#missing_deps[@]} -gt 0 ]]; then
        log_error "Missing required dependencies: ${missing_deps[*]}"
        echo "Please install the missing dependencies and try again." >&2
        exit 1
    fi

    # Check if Docker daemon is running (skip in dry-run mode)
    if [[ "$DRY_RUN" != true ]] && ! docker info >/dev/null 2>&1; then
        log_error "Docker daemon is not running"
        echo "Please start Docker and try again." >&2
        exit 1
    fi

    # Check if buildx is available for multi-platform builds
    if [[ "$PLATFORM" == "multi" ]]; then
        if ! docker buildx version &> /dev/null; then
            log_error "Docker buildx is required for multi-platform builds but not available"
            log_info "Please install Docker Desktop or enable buildx plugin"
            exit 1
        fi

        # Ensure buildx builder is available
        if ! docker buildx inspect &> /dev/null; then
            log_info "Creating buildx builder instance..."
            docker buildx create --use --name multiplatform-builder 2>/dev/null || true
        fi
    fi

    log_verbose "All required tools are available"
}

# --- Local dev server commands ---

DEV_APP_NAME="plugin-skill-hosting"
DEV_PID_FILE="/tmp/${DEV_APP_NAME}.pid"
DEV_LOG_FILE="/tmp/${DEV_APP_NAME}.log"

# Create backend/.env on first run with a real JWT_SECRET so the backend boots
# without manual setup. The default secret in the repo is rejected at startup,
# so a fresh clone can't `start` otherwise. Only written when missing — an
# existing .env (with a real secret) is never touched.
ensure_backend_env() {
    if [[ -f "$BACKEND_DIR/.env" ]]; then
        return
    fi
    if ! command -v openssl >/dev/null 2>&1; then
        log_error "backend/.env is missing and openssl is unavailable to generate a JWT_SECRET."
        log_error "Create $BACKEND_DIR/.env with: JWT_SECRET=\$(openssl rand -hex 32)"
        exit 1
    fi
    printf 'JWT_SECRET=%s\n' "$(openssl rand -hex 32)" > "$BACKEND_DIR/.env"
    log_info "Created $BACKEND_DIR/.env with a generated JWT_SECRET (local dev only)."
}

# Load backend .env if present
load_backend_env() {
    if [[ -f "$BACKEND_DIR/.env" ]]; then
        set -a
        source "$BACKEND_DIR/.env"
        set +a
    fi
}

cmd_dev_start() {
    ensure_backend_env
    load_backend_env
    if [[ -f "$DEV_PID_FILE" ]] && kill -0 "$(cat "$DEV_PID_FILE")" 2>/dev/null; then
        echo "already running (pid $(cat "$DEV_PID_FILE"))"
        return 1
    fi
    echo "building..."
    (cd "$BACKEND_DIR" && go build -o "/tmp/${DEV_APP_NAME}" ./cmd/marketplace)
    echo "starting..."
    nohup "/tmp/${DEV_APP_NAME}" > "$DEV_LOG_FILE" 2>&1 &
    echo $! > "$DEV_PID_FILE"
    echo "started (pid $!, log: $DEV_LOG_FILE)"
}

cmd_dev_stop() {
    if [[ ! -f "$DEV_PID_FILE" ]]; then
        echo "not running"
        return 1
    fi
    local pid
    pid=$(cat "$DEV_PID_FILE")
    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid"
        echo "stopped (pid $pid)"
    else
        echo "stale pid file (process $pid not running)"
    fi
    rm -f "$DEV_PID_FILE"
}

cmd_dev_status() {
    if [[ -f "$DEV_PID_FILE" ]] && kill -0 "$(cat "$DEV_PID_FILE")" 2>/dev/null; then
        echo "running (pid $(cat "$DEV_PID_FILE"))"
    else
        echo "not running"
        rm -f "$DEV_PID_FILE"
    fi
}

cmd_dev_logs() {
    if [[ -f "$DEV_LOG_FILE" ]]; then
        tail -f "$DEV_LOG_FILE"
    else
        echo "no log file found"
    fi
}

cmd_dev_test() {
    log_info "Running backend tests..."
    (cd "$BACKEND_DIR" && go test ./...)
}

cmd_helm_push() {
    local chart_dir="$SCRIPT_DIR/helm/plugin-skill-hosting"
    local registry="ghcr.io/oglimmer"

    local chart_version
    chart_version=$(grep '^version:' "$chart_dir/Chart.yaml" | sed -E 's/version:[[:space:]]*//')

    log_info "Authenticating to GHCR via gh CLI..."
    if ! gh auth token | helm registry login ghcr.io -u "$(gh api user --jq .login)" --password-stdin; then
        log_error "Failed to authenticate to GHCR"
        exit 1
    fi

    log_info "Packaging Helm chart v$chart_version..."
    local tmp_dir
    tmp_dir=$(mktemp -d)
    trap "rm -rf '$tmp_dir'" EXIT

    if ! helm package "$chart_dir" -d "$tmp_dir"; then
        log_error "Failed to package Helm chart"
        exit 1
    fi

    log_info "Pushing Helm chart to oci://$registry..."
    if helm push "$tmp_dir/plugin-skill-hosting-${chart_version}.tgz" "oci://$registry"; then
        log_success "Helm chart v$chart_version pushed to oci://$registry/plugin-skill-hosting"
    else
        log_error "Failed to push Helm chart"
        exit 1
    fi
}

execute_dev_command() {
    case "$DEV_COMMAND" in
        start)      cmd_dev_start ;;
        stop)       cmd_dev_stop ;;
        status)     cmd_dev_status ;;
        logs)       cmd_dev_logs ;;
        test)       cmd_dev_test ;;
        helm-push)  cmd_helm_push ;;
    esac
}

# Show current versions
show_versions() {
    local frontend_version
    frontend_version=$(grep '"version"' "$FRONTEND_DIR/package.json" | head -1 | sed -E 's/.*"version": *"([^"]+)".*/\1/')

    echo "Frontend version: $frontend_version"
}

# Bump semantic version
bump_version() {
    local current_version="$1"
    local bump_type="$2"
    IFS='.' read -r major minor patch <<< "$current_version"

    case "$bump_type" in
        major)
            major=$((major + 1)); minor=0; patch=0;
            ;;
        minor)
            minor=$((minor + 1)); patch=0;
            ;;
        bugfix|patch)
            patch=$((patch + 1));
            ;;
        *)
            echo "Unknown bump type: $bump_type" >&2
            exit 1
            ;;
    esac
    echo "$major.$minor.$patch"
}

# Get platform arguments for docker build
get_platform_args() {
    local platform_args=""

    case "$PLATFORM" in
        "amd64")
            platform_args="--platform linux/amd64"
            ;;
        "arm64")
            platform_args="--platform linux/arm64"
            ;;
        "multi")
            platform_args="--platform linux/amd64,linux/arm64"
            ;;
        "auto"|"")
            # Let Docker detect the platform automatically
            platform_args=""
            ;;
    esac

    echo "$platform_args"
}

# Build Docker image for multiple targets
build_image() {
    local component="$1"
    local dockerfile_args="$2"
    local platform_args=$(get_platform_args)

    # Create array of image tags - passed as remaining arguments
    shift 2
    local image_tags=("$@")
    local primary_tag="${image_tags[0]}"

    log_info "Building $component image for ${#image_tags[@]} target(s):"
    for tag in "${image_tags[@]}"; do
        log_info "  - $tag"
    done
    if [[ -n "$platform_args" ]]; then
        log_info "Target platform(s): $PLATFORM"
    fi

    local build_cmd=""

    # Use buildx for multi-platform builds or when platform is specified
    if [[ "$PLATFORM" == "multi" || (-n "$PLATFORM" && "$PLATFORM" != "auto") ]]; then
        build_cmd="docker buildx build $platform_args"

        # Add all tags
        for tag in "${image_tags[@]}"; do
            build_cmd="$build_cmd --tag $tag"
        done

        if [[ "$PUSH" == true ]]; then
            build_cmd="$build_cmd --push"
        else
            # For local builds with buildx, we need to load the image
            if [[ "$PLATFORM" != "multi" ]]; then
                build_cmd="$build_cmd --load"
            else
                log_warning "Multi-platform builds cannot be loaded locally, forcing push to registry"
                build_cmd="$build_cmd --push"
            fi
        fi

        # Add dockerfile arguments
        build_cmd="$build_cmd $dockerfile_args"

    else
        # Use regular docker build for single platform or auto-detection
        # Build with primary tag first
        build_cmd="docker build $platform_args --tag $primary_tag $dockerfile_args"

        # Tag for additional registries
        if [[ ${#image_tags[@]} -gt 1 ]]; then
            for tag in "${image_tags[@]:1}"; do
                build_cmd="$build_cmd && docker tag $primary_tag $tag"
            done
        fi

        # Push to all registries if requested
        if [[ "$PUSH" == true ]]; then
            for tag in "${image_tags[@]}"; do
                build_cmd="$build_cmd && docker push $tag"
            done
        fi
    fi

    log_verbose "Build command: $build_cmd"

    if execute_cmd "$build_cmd"; then
        log_success "$component image built successfully"
        if [[ "$PUSH" == false && "$PLATFORM" != "multi" ]]; then
            log_info "$component image tagged locally (not pushed)"
        elif [[ "$PUSH" == true ]]; then
            log_success "$component image pushed to ${#image_tags[@]} target(s)"
        fi
    else
        log_error "Failed to build $component image"
        exit 1
    fi
}

# Restart Kubernetes deployment
restart_deployment() {
    local deployment="$1"

    log_info "Restarting deployment: $deployment"

    if execute_cmd "kubectl rollout restart deployment/$deployment"; then
        log_success "Deployment $deployment restarted successfully"

        # Wait for rollout to complete if verbose
        if [[ "$VERBOSE" == true ]]; then
            log_info "Waiting for rollout to complete..."
            kubectl rollout status deployment/"$deployment" --timeout=300s
        fi
    else
        log_error "Failed to restart deployment: $deployment"
        exit 1
    fi
}

# Execute build process
execute_build() {
    # Display configuration
    echo -e "${BOLD}=== Build Configuration ===${RESET}"
    echo "Registries:        ${REGISTRIES[*]}"
    echo "Platform:          ${PLATFORM:-auto}"
    echo "Build Frontend:    $BUILD_FRONTEND"
    echo "Build Backend:     $BUILD_BACKEND"
    echo "Push to Registry:  $PUSH"
    echo "Restart K8s:       $RESTART"
    echo "Dry-run:           $DRY_RUN"
    echo "Verbose:           $VERBOSE"
    if [[ "$BUILD_FRONTEND" == true ]]; then
        echo "Frontend Deploy:   $FRONTEND_DEPLOYMENT"
    fi
    if [[ "$BUILD_BACKEND" == true ]]; then
        echo "Backend Deploy:    $BACKEND_DEPLOYMENT"
    fi
    echo -e "${BOLD}===========================${RESET}"
    echo

    log_info "Starting build process..."

    local app_version git_commit build_time
    app_version=$(grep '"version"' "$FRONTEND_DIR/package.json" | head -1 | sed -E 's/.*"version": *"([^"]+)".*/\1/')
    git_commit=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)

    log_verbose "Build metadata: version=$app_version commit=$git_commit time=$build_time"

    # Build frontend
    if [[ "$BUILD_FRONTEND" == true ]]; then
        build_image "frontend" "--build-arg VITE_APP_VERSION=${app_version} --build-arg VITE_GIT_COMMIT=${git_commit} --build-arg VITE_BUILD_TIME=${build_time} frontend/" "${FRONTEND_IMAGES[@]}"
    fi

    # Build backend
    if [[ "$BUILD_BACKEND" == true ]]; then
        build_image "backend" "--build-arg VERSION=${app_version} --build-arg GIT_COMMIT=${git_commit} --build-arg BUILD_TIME=${build_time} backend/" "${BACKEND_IMAGES[@]}"
    fi

    # Restart deployments if requested
    if [[ "$RESTART" == true ]]; then
        if [[ "$BUILD_FRONTEND" == true ]]; then
            restart_deployment "$FRONTEND_DEPLOYMENT"
        fi

        if [[ "$BUILD_BACKEND" == true ]]; then
            restart_deployment "$BACKEND_DEPLOYMENT"
        fi
    else
        log_info "Skipping deployment restarts (--no-restart specified)"
    fi

    echo
    echo -e "${BOLD}${GREEN}All operations completed successfully${RESET}"
}

# Execute release process
execute_release() {
    log_info "Starting release process..."

    # Show current versions
    echo "Current versions:"; show_versions; echo

    local bump
    if [[ -n "$RELEASE_BUMP" ]]; then
        bump="$RELEASE_BUMP"
        log_info "Bump type from --bump: $bump"
    else
        # Explain bump types
        echo "Select which part to bump (semantic versioning):"
        echo "  1) major  - incompatible API changes"
        echo "  2) minor  - backwards-compatible new features"
        echo "  3) bugfix - backwards-compatible bug fixes"
        PS3="Enter choice (1-3): "
        select bump in major minor bugfix; do
            if [[ -n "$bump" ]]; then
                echo "Chosen bump type: $bump"; break
            else
                echo "Invalid choice. Please select 1, 2, or 3.";
            fi
        done
    fi

    # Compute new version from frontend package.json
    local current_version
    current_version=$(grep '"version"' "$FRONTEND_DIR/package.json" | head -1 | sed -E 's/.*"version": *"([^"]+)".*/\1/')
    local new_version
    new_version=$(bump_version "$current_version" "$bump")
    log_info "Releasing version $new_version..."

    # Update frontend version
    log_info "Updating frontend version to $new_version..."
    (cd "$FRONTEND_DIR" && npm version "$new_version" --no-git-tag-version)

    # Update Helm chart version and appVersion
    local chart_file="$SCRIPT_DIR/helm/plugin-skill-hosting/Chart.yaml"
    log_info "Updating Helm chart version to $new_version..."
    sed -i '' "s/^version:.*/version: $new_version/" "$chart_file"
    sed -i '' "s/^appVersion:.*/appVersion: \"$new_version\"/" "$chart_file"

    # Commit, tag, and push — the tag push triggers the GitHub Actions release workflow
    log_info "Committing version changes and creating tag..."
    git add "$FRONTEND_DIR/package.json" "$FRONTEND_DIR/package-lock.json" "$chart_file"
    git commit -m "Release v$new_version"
    git tag -a "v$new_version" -m "Release v$new_version"

    log_info "Pushing commit and tag to origin..."
    git push origin HEAD
    git push origin "v$new_version"

    log_success "Release v$new_version tagged and pushed. GitHub Actions will build and publish the images."

    cmd_helm_push
}

# Main execution function
main() {
    # Show help if no arguments provided
    if [[ $# -eq 0 ]]; then
        show_help
        exit 0
    fi

    parse_args "$@"

    if [[ "$HELP" == true ]]; then
        show_help
        exit 0
    fi

    if [[ "$SHOW_VERSIONS" == true ]]; then
        show_versions
        exit 0
    fi

    if [[ -n "$DEV_COMMAND" ]]; then
        execute_dev_command
        exit 0
    fi

    check_prerequisites

    if [[ "$RELEASE_MODE" == true ]]; then
        execute_release
    else
        execute_build
    fi
}

# Run main function with all arguments
main "$@"
