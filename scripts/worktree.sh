#!/usr/bin/env bash
set -euo pipefail

# Balance Git Worktree Manager
# Usage:
#   ./scripts/worktree.sh create <name>
#   ./scripts/worktree.sh clean <name> [--delete-branch]
#   ./scripts/worktree.sh update-maintree <name>
#   ./scripts/worktree.sh update-branch

# Find repository root
MAIN_GIT_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
CURRENT_GIT_DIR="$(git rev-parse --git-dir 2>/dev/null || true)"
TOP_LEVEL="$(git rev-parse --show-toplevel)"

is_main_tree() {
    # In main tree, .git is a directory and git-common-dir == .git or same as git-dir
    if [[ -d "${TOP_LEVEL}/.git" ]]; then
        return 0
    else
        return 1
    fi
}

find_free_port() {
    local start_port=10000
    local end_port=11000
    local port

    # Try random ports in range first, then sequential
    for _ in {1..50}; do
        port=$(( start_port + RANDOM % (end_port - start_port + 1) ))
        if ! ss -tuln | grep -q ":${port} "; then
            echo "$port"
            return 0
        fi
    done

    # Fallback to sequential scan
    for ((port=start_port; port<=end_port; port++)); do
        if ! ss -tuln | grep -q ":${port} "; then
            echo "$port"
            return 0
        fi
    done

    echo "Error: No free port found in range ${start_port}-${end_port}" >&2
    exit 1
}

cmd_create() {
    local name="${1:-}"
    if [[ -z "$name" ]]; then
        echo "Error: worktree name is required. Usage: task dev:worktree:create -- <name>" >&2
        exit 1
    fi

    local target_dir="../development/${name}"
    local abs_target_dir
    abs_target_dir="$(readlink -m "${TOP_LEVEL}/../development/${name}")"

    if [[ -d "$abs_target_dir" ]]; then
        echo "Error: Directory ${target_dir} already exists!" >&2
        exit 1
    fi

    # Create parent development directory if not exists
    mkdir -p "$(dirname "$abs_target_dir")"

    # Check if branch exists
    if git show-ref --verify --quiet "refs/heads/${name}"; then
        echo "Adding worktree for existing branch '${name}'..."
        git worktree add "$abs_target_dir" "$name"
    else
        echo "Creating branch '${name}' and adding worktree..."
        git worktree add -b "$name" "$abs_target_dir" HEAD
    fi

    # Copy .env
    local free_port
    free_port="$(find_free_port)"

    local source_env="${TOP_LEVEL}/.env"
    if [[ ! -f "$source_env" ]]; then
        if [[ -f "${TOP_LEVEL}/.env.example" ]]; then
            source_env="${TOP_LEVEL}/.env.example"
        fi
    fi

    local dest_env="${abs_target_dir}/.env"
    if [[ -f "$source_env" ]]; then
        cp "$source_env" "$dest_env"
        # Replace or append PORT
        if grep -q "^PORT=" "$dest_env"; then
            sed -i "s/^PORT=.*/PORT=${free_port}/" "$dest_env"
        else
            echo "PORT=${free_port}" >> "$dest_env"
        fi
    else
        echo "PORT=${free_port}" > "$dest_env"
        echo "DATA_DIR=./data" >> "$dest_env"
    fi

    # Copy data directory (excluding lock/temp files)
    local data_dir_rel="./data"
    if [[ -f "$dest_env" ]] && grep -q "^DATA_DIR=" "$dest_env"; then
        data_dir_rel="$(grep "^DATA_DIR=" "$dest_env" | cut -d'=' -f2- | tr -d '"' | tr -d "'")"
    fi

    local abs_source_data="${TOP_LEVEL}/${data_dir_rel#./}"
    local abs_dest_data="${abs_target_dir}/${data_dir_rel#./}"

    if [[ -d "$abs_source_data" ]]; then
        echo "Copying data directory from ${abs_source_data} to ${abs_dest_data}..."
        mkdir -p "$abs_dest_data"
        # Copy files excluding -wal and -shm
        rsync -a --exclude="*.db-wal" --exclude="*.db-shm" "${abs_source_data}/" "${abs_dest_data}/" 2>/dev/null || \
        cp -R "${abs_source_data}/." "${abs_dest_data}/"
        rm -f "${abs_dest_data}"/*.db-wal "${abs_dest_data}"/*.db-shm 2>/dev/null || true
    else
        mkdir -p "${abs_dest_data}/budgets"
    fi

    # Copy .agents directory and skills-lock.json if present
    if [[ -d "${TOP_LEVEL}/.agents" ]]; then
        echo "Copying .agents directory to ${abs_target_dir}/.agents..."
        mkdir -p "${abs_target_dir}/.agents"
        rsync -a "${TOP_LEVEL}/.agents/" "${abs_target_dir}/.agents/" 2>/dev/null || \
        cp -R "${TOP_LEVEL}/.agents/." "${abs_target_dir}/.agents/"
    fi

    if [[ -f "${TOP_LEVEL}/skills-lock.json" ]]; then
        cp "${TOP_LEVEL}/skills-lock.json" "${abs_target_dir}/skills-lock.json"
    fi

    echo "Worktree '${name}' successfully created at ${target_dir}!"
    echo "Allocated port: ${free_port}"
    echo "To use: cd ${target_dir} && task dev:up"
}

cmd_clean() {
    local name="${1:-}"
    local delete_branch="false"

    shift || true
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --delete-branch|DELETE_BRANCH=true|true)
                delete_branch="true"
                shift
                ;;
            *)
                shift
                ;;
        esac
    done

    if [[ -z "$name" ]]; then
        echo "Error: worktree name is required. Usage: task dev:worktree:clean -- <name> [--delete-branch]" >&2
        exit 1
    fi

    local abs_target_dir
    abs_target_dir="$(readlink -m "${TOP_LEVEL}/../development/${name}")"

    if [[ -d "$abs_target_dir" ]] || git worktree list | grep -q "$abs_target_dir"; then
        echo "Removing worktree at ${abs_target_dir}..."
        git worktree remove --force "$abs_target_dir" 2>/dev/null || rm -rf "$abs_target_dir"
        git worktree prune
    else
        echo "Worktree at ${abs_target_dir} not found in git worktree list."
    fi

    if [[ "$delete_branch" == "true" ]]; then
        if git show-ref --verify --quiet "refs/heads/${name}"; then
            echo "Deleting branch '${name}'..."
            git branch -D "$name"
        fi
    else
        echo "Branch '${name}' preserved (pass --delete-branch to delete branch as well)."
    fi

    echo "Clean operation for '${name}' completed."
}

cmd_update_maintree() {
    local name="${1:-}"
    if ! is_main_tree; then
        echo "Error: dev:worktree:update:maintree can only be run from the main repository tree!" >&2
        exit 1
    fi

    if [[ -z "$name" ]]; then
        echo "Error: branch/worktree name is required. Usage: task dev:worktree:update:maintree -- <name>" >&2
        exit 1
    fi

    if ! git show-ref --verify --quiet "refs/heads/${name}"; then
        echo "Error: Branch '${name}' does not exist!" >&2
        exit 1
    fi

    echo "Fast-forwarding main tree with branch '${name}'..."
    git merge --ff-only "$name"
    echo "Main tree successfully updated from '${name}'."
}

cmd_update_branch() {
    if is_main_tree; then
        echo "Error: dev:worktree:update:branch must be run from inside a secondary worktree, not maintree!" >&2
        exit 1
    fi

    local current_branch
    current_branch="$(git rev-parse --abbrev-ref HEAD)"

    echo "Rebasing worktree branch '${current_branch}' onto main..."
    git rebase main
    echo "Worktree branch '${current_branch}' successfully rebased on main."
}

# Main routing
ACTION="${1:-}"
shift || true

case "$ACTION" in
    create)
        cmd_create "$@"
        ;;
    clean)
        cmd_clean "$@"
        ;;
    update-maintree)
        cmd_update_maintree "$@"
        ;;
    update-branch)
        cmd_update_branch "$@"
        ;;
    *)
        echo "Usage: $0 {create <name>|clean <name> [--delete-branch]|update-maintree <name>|update-branch}" >&2
        exit 1
        ;;
esac
