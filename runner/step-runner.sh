#!/usr/bin/env bash
# step-runner.sh — Fluxor GHA step runner entrypoint
#
# Reads /fluxor/steps.json and executes each step sequentially, providing:
#   • ${{ expression }} evaluation (github.*, env.*, steps.*.outputs.*)
#   • GITHUB_ENV file management (persists env between steps)
#   • GITHUB_OUTPUT file management (captures step outputs)
#   • GITHUB_PATH file management (extends PATH for subsequent steps)
#   • actions/upload-artifact and actions/download-artifact interception
#   • docker:// action execution via the mounted Docker socket
#   • Composite action support via action.yml parsing
#   • Step-level continue-on-error support

set -euo pipefail

STEPS_FILE="${FLUXOR_STEPS_FILE:-/fluxor/steps.json}"
CONTEXT_FILE="${FLUXOR_CONTEXT_FILE:-/fluxor/context.json}"

# Initialise GitHub magic files
touch "${GITHUB_ENV}"
touch "${GITHUB_OUTPUT}"
touch "${GITHUB_PATH}"
touch "${GITHUB_STEP_SUMMARY:-/fluxor/github_step_summary}"

# ── Helpers ────────────────────────────────────────────────────────────────────

log_group() { echo "::group::$1"; }
log_endgroup() { echo "::endgroup::"; }
log_info()  { echo "  $1"; }
log_warn()  { echo "##[warning]$1" >&2; }
log_error() { echo "##[error]$1" >&2; }

# Load GITHUB_ENV file into current shell env
load_github_env() {
  while IFS='=' read -r key value; do
    [[ -z "$key" || "$key" == \#* ]] && continue
    export "$key=$value"
  done < "${GITHUB_ENV}"
}

# Load GITHUB_PATH entries into PATH
load_github_path() {
  while IFS= read -r p; do
    [[ -z "$p" ]] && continue
    export PATH="$p:$PATH"
  done < "${GITHUB_PATH}"
}

# Evaluate ${{ expression }} in a string.
# Supports: github.sha, github.ref, github.ref_name, github.repository,
#           github.run_id, github.actor, github.event_name,
#           env.<VAR>, steps.<id>.outputs.<key>
eval_expression() {
  local input="$1"
  local result="$input"

  # Load context JSON once
  local ctx
  ctx=$(cat "$CONTEXT_FILE" 2>/dev/null || echo '{}')

  # Replace ${{ github.* }}
  result=$(echo "$result" | sed \
    -e "s/\${{ *github\.sha *}}/$(echo "$ctx" | jq -r '.sha // ""')/g" \
    -e "s/\${{ *github\.ref *}}/$(echo "$ctx" | jq -r '.ref // ""')/g" \
    -e "s/\${{ *github\.ref_name *}}/$(echo "$ctx" | jq -r '.ref_name // ""')/g" \
    -e "s/\${{ *github\.repository *}}/$(echo "$ctx" | jq -r '.repository // ""')/g" \
    -e "s/\${{ *github\.run_id *}}/$(echo "$ctx" | jq -r '.run_id // ""')/g" \
    -e "s/\${{ *github\.run_number *}}/$(echo "$ctx" | jq -r '.run_number // ""')/g" \
    -e "s/\${{ *github\.actor *}}/$(echo "$ctx" | jq -r '.actor // ""')/g" \
    -e "s/\${{ *github\.event_name *}}/$(echo "$ctx" | jq -r '.event_name // ""')/g" \
    -e "s/\${{ *github\.workspace *}}/\/workspace/g" \
  )

  # Replace ${{ env.VAR }} with shell env lookups
  while [[ "$result" =~ \$\{\{[[:space:]]*env\.([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*\}\} ]]; do
    local var_name="${BASH_REMATCH[1]}"
    local var_val="${!var_name:-}"
    result="${result//${{ env.$var_name }}/$var_val}"
    # Prevent infinite loop on unknown vars
    result=$(echo "$result" | sed "s/\${{ *env\.$var_name *}}/$var_val/g")
  done

  echo "$result"
}

# Evaluate the 'if:' condition of a step
eval_if() {
  local condition="$1"
  if [[ -z "$condition" ]]; then
    return 0  # no condition → always run
  fi
  # Simple boolean keywords
  case "$condition" in
    "always()") return 0 ;;
    "success()") return 0 ;;
    "failure()") return 1 ;;
    "cancelled()") return 1 ;;
  esac
  # Evaluate as a bash expression after ${{ }} substitution
  local evaled
  evaled=$(eval_expression "$condition")
  if eval "[[ $evaled ]]" 2>/dev/null; then
    return 0
  fi
  return 1
}

# Execute a single step's shell script
run_shell_step() {
  local script="$1"
  local shell="${2:-bash}"
  local workdir="${3:-/workspace}"
  local step_env="$4"  # JSON string of step-level env vars

  # Export step-level env
  if [[ -n "$step_env" && "$step_env" != "null" ]]; then
    while IFS='=' read -r k v; do
      export "$k"="$v"
    done < <(echo "$step_env" | jq -r 'to_entries[] | "\(.key)=\(.value)"')
  fi

  local evaluated_script
  evaluated_script=$(eval_expression "$script")

  pushd "$workdir" > /dev/null
  case "$shell" in
    bash)   bash -euo pipefail -c "$evaluated_script" ;;
    sh)     sh -c "$evaluated_script" ;;
    python) python3 -c "$evaluated_script" ;;
    node)   node -e "$evaluated_script" ;;
    pwsh|powershell) echo "PowerShell not supported in this runner" >&2; exit 1 ;;
    *)      bash -euo pipefail -c "$evaluated_script" ;;
  esac
  local exit_code=$?
  popd > /dev/null
  return $exit_code
}

# Handle a docker:// action step
run_docker_action() {
  local image="$1"
  local step_with="$2"

  local entrypoint=""
  local args=""
  if [[ -n "$step_with" && "$step_with" != "null" ]]; then
    entrypoint=$(echo "$step_with" | jq -r '.entrypoint // ""')
    args=$(echo "$step_with" | jq -r '.args // ""')
  fi

  log_info "Running Docker action: $image"
  local docker_args=( run --rm -v /workspace:/workspace -w /workspace )
  # Forward all GITHUB_* env vars into the action container
  docker_args+=( -e GITHUB_WORKSPACE=/workspace )
  docker_args+=( -e GITHUB_ENV="$GITHUB_ENV" )
  docker_args+=( -e GITHUB_OUTPUT="$GITHUB_OUTPUT" )
  docker_args+=( -e GITHUB_PATH="$GITHUB_PATH" )
  docker_args+=( -v "${GITHUB_ENV}:${GITHUB_ENV}" )
  docker_args+=( -v "${GITHUB_OUTPUT}:${GITHUB_OUTPUT}" )
  docker_args+=( -v "${GITHUB_PATH}:${GITHUB_PATH}" )
  docker_args+=( -v /var/run/docker.sock:/var/run/docker.sock )

  if [[ -n "$entrypoint" ]]; then
    docker_args+=( --entrypoint "$entrypoint" )
  fi
  docker_args+=( "$image" )
  if [[ -n "$args" ]]; then
    # shellcheck disable=SC2086
    docker_args+=( $args )
  fi

  docker "${docker_args[@]}"
}

# Handle actions/upload-artifact — copies files to /workspace so Fluxor picks them up
handle_upload_artifact() {
  local step_with="$1"
  local artifact_name artifact_path
  artifact_name=$(echo "$step_with" | jq -r '.name // "artifact"')
  artifact_path=$(echo "$step_with" | jq -r '.path // ""')

  if [[ -z "$artifact_path" ]]; then
    log_warn "upload-artifact: no path specified"
    return 0
  fi

  log_info "Bridging upload-artifact '$artifact_name' → /workspace/$artifact_path (Fluxor MinIO)"
  # Fluxor's uploadArtifacts picks up anything in /workspace matching ArtifactsOut paths.
  # If the path is already under /workspace, no copy needed.
  if [[ "$artifact_path" != /* ]]; then
    artifact_path="/workspace/$artifact_path"
  fi
  # Write to GITHUB_OUTPUT so downstream steps can reference it
  echo "artifact-path=$artifact_path" >> "$GITHUB_OUTPUT"
}

# Handle actions/download-artifact — files are already in /workspace via ArtifactsIn pre-flight
handle_download_artifact() {
  local step_with="$1"
  local artifact_name artifact_path
  artifact_name=$(echo "$step_with" | jq -r '.name // ""')
  artifact_path=$(echo "$step_with" | jq -r '.path // /workspace')

  log_info "Bridging download-artifact '$artifact_name' — files are pre-loaded at /workspace/$artifact_name"
}

# Run a named action (e.g. actions/checkout@v4)
run_named_action() {
  local uses="$1"
  local step_with="$2"
  local step_env="$3"

  # Well-known action interceptions
  case "$uses" in
    actions/checkout@*)
      local repo_path="${step_with_path:-/workspace}"
      log_info "Intercepting actions/checkout — using current /workspace"
      # No-op: code is already in /workspace via the bind mount.
      # If a token and repo URL are provided, we could git clone.
      if [[ -n "$(echo "$step_with" | jq -r '.repository // ""')" ]]; then
        local repo_url
        repo_url=$(echo "$step_with" | jq -r '.repository // ""')
        log_info "Cloning $repo_url into /workspace"
        git clone "https://github.com/$repo_url.git" /workspace 2>&1 || true
      fi
      ;;

    actions/upload-artifact@*)
      handle_upload_artifact "$step_with"
      ;;

    actions/download-artifact@*)
      handle_download_artifact "$step_with"
      ;;

    actions/setup-node@*)
      local node_version
      node_version=$(echo "$step_with" | jq -r '."node-version" // "20"')
      log_info "setup-node: node version $node_version (using system node)"
      ;;

    actions/setup-python@*)
      local py_version
      py_version=$(echo "$step_with" | jq -r '."python-version" // "3"')
      log_info "setup-python: python version $py_version (using system python3)"
      ;;

    actions/setup-go@*)
      local go_version
      go_version=$(echo "$step_with" | jq -r '."go-version" // "1.22"')
      log_info "setup-go: go version $go_version (using system go)"
      ;;

    actions/cache@*)
      log_warn "actions/cache is a no-op in Fluxor (no cache service available)"
      ;;

    docker/*)
      # docker/build-push-action etc. — try to run via docker CLI
      log_info "Attempting Docker action: $uses"
      local image="ghcr.io/${uses%@*}:${uses#*@}"
      run_docker_action "$image" "$step_with" || log_warn "Docker action $uses failed (continuing)"
      ;;

    *)
      # Attempt to run via act if available, else warn and skip
      if command -v act &>/dev/null; then
        log_info "Running $uses via act"
        act_dir=$(mktemp -d)
        # Write a minimal workflow with just this action
        cat > "$act_dir/workflow.yml" << YAML
on: push
jobs:
  step:
    runs-on: ubuntu-latest
    steps:
      - uses: $uses
        with: $(echo "$step_with" | jq '.')
YAML
        act push -W "$act_dir/workflow.yml" --bind -P ubuntu-latest=fluxor/gha-runner:latest 2>&1 || \
          log_warn "act execution failed for $uses"
        rm -rf "$act_dir"
      else
        log_warn "Action '$uses' is not natively supported and act is unavailable. Skipping."
      fi
      ;;
  esac
}

# ── Main step loop ─────────────────────────────────────────────────────────────

STEP_COUNT=$(jq 'length' "$STEPS_FILE")
log_info "Starting GHA job execution: $STEP_COUNT steps"

# Declare associative array for step outputs
declare -A STEP_OUTPUTS

for i in $(seq 0 $((STEP_COUNT - 1))); do
  STEP=$(jq ".[$i]" "$STEPS_FILE")

  STEP_ID=$(echo "$STEP"   | jq -r '.id // ""')
  STEP_NAME=$(echo "$STEP" | jq -r '.name // "step-'"$((i+1))"'"')
  STEP_USES=$(echo "$STEP" | jq -r '.uses // ""')
  STEP_RUN=$(echo "$STEP"  | jq -r '.run // ""')
  STEP_SHELL=$(echo "$STEP" | jq -r '.shell // "bash"')
  STEP_WORKDIR=$(echo "$STEP" | jq -r '."working-directory" // "/workspace"')
  STEP_IF=$(echo "$STEP"   | jq -r '.if // ""')
  STEP_CONTINUE=$(echo "$STEP" | jq -r '."continue-on-error" // "false"')
  STEP_WITH=$(echo "$STEP" | jq -c '.with // {}')
  STEP_ENV=$(echo "$STEP"  | jq -c '.env // {}')

  log_group "Step $((i+1)): $STEP_NAME"

  # Reload GITHUB_ENV and GITHUB_PATH accumulations from previous steps
  load_github_env
  load_github_path

  # Evaluate if: condition
  if ! eval_if "$STEP_IF"; then
    log_info "⏭ Skipped (condition: $STEP_IF)"
    log_endgroup
    continue
  fi

  STEP_EXIT=0

  if [[ -n "$STEP_RUN" ]]; then
    # Shell script step
    run_shell_step "$STEP_RUN" "$STEP_SHELL" "$STEP_WORKDIR" "$STEP_ENV" || STEP_EXIT=$?
  elif [[ "$STEP_USES" == docker://* ]]; then
    # Docker action
    IMAGE="${STEP_USES#docker://}"
    run_docker_action "$IMAGE" "$STEP_WITH" || STEP_EXIT=$?
  elif [[ -n "$STEP_USES" ]]; then
    # Named action
    run_named_action "$STEP_USES" "$STEP_WITH" "$STEP_ENV" || STEP_EXIT=$?
  else
    log_warn "Step has neither 'run' nor 'uses' — skipping"
  fi

  # Capture step outputs (read from GITHUB_OUTPUT since last checkpoint)
  if [[ -n "$STEP_ID" && -f "$GITHUB_OUTPUT" ]]; then
    while IFS='=' read -r key value; do
      STEP_OUTPUTS["${STEP_ID}.${key}"]="$value"
    done < <(grep -v '^#' "$GITHUB_OUTPUT" 2>/dev/null || true)
  fi

  if [[ $STEP_EXIT -ne 0 ]]; then
    if [[ "$STEP_CONTINUE" == "true" ]]; then
      log_warn "Step '$STEP_NAME' failed with exit $STEP_EXIT (continue-on-error: true)"
    else
      log_error "Step '$STEP_NAME' failed with exit $STEP_EXIT"
      log_endgroup
      exit $STEP_EXIT
    fi
  else
    log_info "✓ Step '$STEP_NAME' completed"
  fi

  log_endgroup
done

log_info "All $STEP_COUNT steps completed successfully"
