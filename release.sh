#!/bin/bash
# release.sh - Release script for ori-shell-executor plugin
# Usage: ./release.sh [options]

set -e

# Configuration
PLUGIN_NAME="ori-shell-executor"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ORI_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ORI_AGENT_DIR="$ORI_ROOT/ori-agent"

# Options
DRY_RUN=false
SKIP_TESTS=false
SKIP_PUSH=false
SKIP_GITHUB_RELEASE=false
UPDATE_REGISTRY=false
VERSION_OVERRIDE=""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --version)
            VERSION_OVERRIDE="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --skip-tests)
            SKIP_TESTS=true
            shift
            ;;
        --skip-push)
            SKIP_PUSH=true
            shift
            ;;
        --skip-github-release)
            SKIP_GITHUB_RELEASE=true
            shift
            ;;
        --update-registry)
            UPDATE_REGISTRY=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [options]"
            echo ""
            echo "Options:"
            echo "  --version <version>       Override version (default: read from plugin.yaml)"
            echo "  --dry-run                 Show what would be done without doing it"
            echo "  --skip-tests              Skip running tests"
            echo "  --skip-push               Skip pushing to remote"
            echo "  --skip-github-release     Skip creating GitHub release"
            echo "  --update-registry         Update ori-agent plugin registry"
            echo "  --help                    Show this help"
            echo ""
            echo "Examples:"
            echo "  $0                        # Release using version from plugin.yaml"
            echo "  $0 --version v0.0.5       # Release with specific version"
            echo "  $0 --dry-run              # Preview what would happen"
            echo "  $0 --update-registry      # Also update the plugin registry"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Helper functions
log_info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

log_success() {
    echo -e "${GREEN}✓${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

log_error() {
    echo -e "${RED}✗${NC} $1"
}

run_cmd() {
    if [ "$DRY_RUN" = true ]; then
        echo -e "${YELLOW}[DRY RUN]${NC} $*"
    else
        "$@"
    fi
}

# Get version from plugin.yaml
get_version() {
    if [ -n "$VERSION_OVERRIDE" ]; then
        echo "$VERSION_OVERRIDE"
    else
        grep '^version:' plugin.yaml | awk '{print $2}' | tr -d '"' | tr -d "'"
    fi
}

# Ensure version has 'v' prefix
ensure_v_prefix() {
    local ver=$1
    if [[ ! "$ver" =~ ^v ]]; then
        echo "v$ver"
    else
        echo "$ver"
    fi
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check we're in the right directory
    if [ ! -f "plugin.yaml" ]; then
        log_error "plugin.yaml not found. Run this script from the plugin directory."
        exit 1
    fi

    # Check Go is installed
    if ! command -v go &> /dev/null; then
        log_error "Go is not installed"
        exit 1
    fi

    # Check gh CLI is installed (for GitHub releases)
    if [ "$SKIP_GITHUB_RELEASE" = false ]; then
        if ! command -v gh &> /dev/null; then
            log_error "GitHub CLI (gh) is not installed. Install with: brew install gh"
            exit 1
        fi

        # Check gh is authenticated
        if ! gh auth status &> /dev/null; then
            log_error "GitHub CLI is not authenticated. Run: gh auth login"
            exit 1
        fi
    fi

    # Check for uncommitted changes
    if ! git diff --quiet || ! git diff --cached --quiet; then
        log_warning "You have uncommitted changes"
        git status --short
        if [ "$DRY_RUN" = false ]; then
            echo ""
            read -p "Continue anyway? (y/N) " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                exit 1
            fi
        fi
    fi

    log_success "Prerequisites check passed"
}

# Run tests
run_tests() {
    if [ "$SKIP_TESTS" = true ]; then
        log_warning "Skipping tests"
        return
    fi

    log_info "Running tests..."

    if [ "$DRY_RUN" = true ]; then
        echo -e "${YELLOW}[DRY RUN]${NC} go test -v ./..."
    else
        if go test -v ./... 2>&1; then
            log_success "Tests passed"
        else
            # Check if there are no test files (not an error)
            if go test ./... 2>&1 | grep -q "no test files"; then
                log_warning "No test files found (this is okay)"
            else
                log_error "Tests failed"
                exit 1
            fi
        fi
    fi
}

# Generate code from plugin.yaml
generate_code() {
    log_info "Generating code from plugin.yaml..."

    if [ ! -f "$ORI_AGENT_DIR/bin/ori-plugin-gen" ]; then
        log_warning "ori-plugin-gen not found, building it first..."
        if [ "$DRY_RUN" = false ]; then
            (cd "$ORI_AGENT_DIR" && go build -o bin/ori-plugin-gen ./cmd/ori-plugin-gen)
        fi
    fi

    run_cmd go generate ./...
    log_success "Code generation complete"
}

# Build for all platforms
build_all_platforms() {
    log_info "Building for all platforms..."

    local platforms=(
        "darwin/amd64"
        "darwin/arm64"
        "linux/amd64"
        "linux/arm64"
        "windows/amd64"
        "windows/arm64"
    )

    for platform in "${platforms[@]}"; do
        local os="${platform%/*}"
        local arch="${platform#*/}"
        local output="${PLUGIN_NAME}-${os}-${arch}"

        if [ "$os" = "windows" ]; then
            output="${output}.exe"
        fi

        log_info "  Building ${os}/${arch}..."

        if [ "$DRY_RUN" = true ]; then
            echo -e "${YELLOW}[DRY RUN]${NC} GOOS=$os GOARCH=$arch go build -ldflags=\"-s -w\" -o $output ."
        else
            GOOS="$os" GOARCH="$arch" go build -ldflags="-s -w" -o "$output" .
            log_success "  Built: $output"
        fi
    done

    log_success "All platforms built"
}

# Generate checksums
generate_checksums() {
    log_info "Generating SHA256 checksums..."

    if [ "$DRY_RUN" = true ]; then
        echo -e "${YELLOW}[DRY RUN]${NC} shasum -a 256 ${PLUGIN_NAME}-* > SHA256SUMS"
    else
        shasum -a 256 ${PLUGIN_NAME}-* > SHA256SUMS
        log_success "Checksums generated:"
        cat SHA256SUMS
    fi
}

# Create and push git tag
create_tag() {
    local version=$1

    log_info "Creating git tag: $version"

    # Check if tag already exists
    if git rev-parse "$version" &> /dev/null; then
        log_warning "Tag $version already exists"
        read -p "Delete and recreate? (y/N) " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            run_cmd git tag -d "$version"
            run_cmd git push origin ":refs/tags/$version" 2>/dev/null || true
        else
            return 1
        fi
    fi

    run_cmd git tag -a "$version" -m "Release $version"
    log_success "Tag created: $version"

    if [ "$SKIP_PUSH" = false ]; then
        log_info "Pushing tag to remote..."
        run_cmd git push origin "$version"
        log_success "Tag pushed to remote"
    fi
}

# Create GitHub release
create_github_release() {
    local version=$1

    if [ "$SKIP_GITHUB_RELEASE" = true ]; then
        log_warning "Skipping GitHub release"
        return
    fi

    log_info "Creating GitHub release..."

    # Get release notes from git log
    local prev_tag=$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || echo "")
    local release_notes=""

    if [ -n "$prev_tag" ]; then
        release_notes=$(git log --pretty=format:"- %s" "$prev_tag"..HEAD 2>/dev/null || echo "")
    fi

    if [ -z "$release_notes" ]; then
        release_notes="Release $version"
    fi

    local release_body="## What's Changed

$release_notes

## Installation

Download the appropriate binary for your platform and place it in your ori-agent plugins directory.

### Checksums
\`\`\`
$(cat SHA256SUMS)
\`\`\`
"

    if [ "$DRY_RUN" = true ]; then
        echo -e "${YELLOW}[DRY RUN]${NC} gh release create $version ${PLUGIN_NAME}-* SHA256SUMS --title \"$version\" --notes \"...\""
        echo ""
        echo "Release notes would be:"
        echo "$release_body"
    else
        gh release create "$version" \
            ${PLUGIN_NAME}-darwin-amd64 \
            ${PLUGIN_NAME}-darwin-arm64 \
            ${PLUGIN_NAME}-linux-amd64 \
            ${PLUGIN_NAME}-linux-arm64 \
            ${PLUGIN_NAME}-windows-amd64.exe \
            ${PLUGIN_NAME}-windows-arm64.exe \
            SHA256SUMS \
            --title "$version" \
            --notes "$release_body"

        log_success "GitHub release created"
        echo ""
        echo "Release URL: https://github.com/johnjallday/${PLUGIN_NAME}/releases/tag/$version"
    fi
}

# Update plugin registry
update_plugin_registry() {
    local version=$1

    if [ "$UPDATE_REGISTRY" = false ]; then
        return
    fi

    log_info "Updating plugin registry..."

    local registry_updater="$ORI_ROOT/scripts/ci-cd/update-plugin-registry.sh"

    if [ ! -f "$registry_updater" ]; then
        log_warning "Registry updater not found at: $registry_updater"
        return
    fi

    if [ "$DRY_RUN" = true ]; then
        run_cmd "$registry_updater" --plugin "$PLUGIN_NAME" --version "$version" --dry-run
    else
        run_cmd "$registry_updater" --plugin "$PLUGIN_NAME" --version "$version"
    fi

    log_success "Plugin registry updated"
}

# Cleanup build artifacts
cleanup() {
    log_info "Cleaning up build artifacts..."
    rm -f ${PLUGIN_NAME}-darwin-* ${PLUGIN_NAME}-linux-* ${PLUGIN_NAME}-windows-* SHA256SUMS 2>/dev/null || true
    log_success "Cleanup complete"
}

# Main release flow
main() {
    echo ""
    echo "========================================"
    echo "  ${PLUGIN_NAME} Release Script"
    echo "========================================"

    cd "$SCRIPT_DIR"

    # Get version
    VERSION=$(get_version)
    VERSION=$(ensure_v_prefix "$VERSION")

    echo "Version: $VERSION"
    echo "Dry Run: $DRY_RUN"
    echo "Skip Tests: $SKIP_TESTS"
    echo "Skip Push: $SKIP_PUSH"
    echo "Skip GitHub Release: $SKIP_GITHUB_RELEASE"
    echo "Update Registry: $UPDATE_REGISTRY"
    echo "========================================"
    echo ""

    # Confirmation
    if [ "$DRY_RUN" = false ]; then
        read -p "Proceed with release $VERSION? (y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            echo "Aborted."
            exit 0
        fi
        echo ""
    fi

    # Run release steps
    check_prerequisites
    echo ""

    run_tests
    echo ""

    generate_code
    echo ""

    build_all_platforms
    echo ""

    generate_checksums
    echo ""

    create_tag "$VERSION"
    echo ""

    create_github_release "$VERSION"
    echo ""

    update_plugin_registry "$VERSION"
    echo ""

    # Cleanup (optional - comment out to keep artifacts)
    # cleanup

    echo "========================================"
    echo -e "  ${GREEN}Release Complete!${NC}"
    echo "========================================"
    echo ""
    echo "Next steps:"
    echo "  1. Verify the GitHub release: https://github.com/johnjallday/${PLUGIN_NAME}/releases"
    if [ "$UPDATE_REGISTRY" = true ]; then
        echo "  2. Commit and push registry changes in ori-agent"
    else
        echo "  2. Run with --update-registry to update ori-agent plugin registry"
    fi
    echo ""
}

# Run main function
main
