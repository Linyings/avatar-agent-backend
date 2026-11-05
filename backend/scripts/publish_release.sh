#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
通过创建并推送版本标签发布后端新版本。

用法：
  scripts/publish_release.sh <version> [options]

示例：
  scripts/publish_release.sh 1.2.3
  scripts/publish_release.sh v1.2.3 --skip-tests
  DRY_RUN=1 scripts/publish_release.sh 1.2.3

选项：
  --skip-tests              跳过本地 go test ./...
  --remote <name>           指定要推送的 Git remote，默认 origin。
  --create-github-release   推送标签后，额外使用 gh 创建 GitHub Release。
  --dry-run                 只打印将要执行的命令，不修改 Git 状态。
  -h, --help                显示帮助信息。

环境变量：
  REMOTE                    指定要推送的 Git remote，默认 origin。
  SKIP_TESTS=1              跳过本地 go test ./...
  CREATE_GITHUB_RELEASE=1   推送标签后，额外使用 gh 创建 GitHub Release。
  DRY_RUN=1                 只打印将要执行的命令，不修改 Git 状态。
EOF
}

die() {
  echo "错误：$*" >&2
  exit 1
}

run() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
  if [ "${DRY_RUN}" != "1" ]; then
    "$@"
  fi
}

version=""
REMOTE="${REMOTE:-origin}"
SKIP_TESTS="${SKIP_TESTS:-0}"
CREATE_GITHUB_RELEASE="${CREATE_GITHUB_RELEASE:-0}"
DRY_RUN="${DRY_RUN:-0}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --skip-tests)
      SKIP_TESTS=1
      ;;
    --remote)
      shift
      [ "$#" -gt 0 ] || die "--remote 需要指定 remote 名称"
      REMOTE="$1"
      ;;
    --create-github-release)
      CREATE_GITHUB_RELEASE=1
      ;;
    --dry-run)
      DRY_RUN=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      die "未知选项：$1"
      ;;
    *)
      [ -z "${version}" ] || die "只能指定一个版本号"
      version="$1"
      ;;
  esac
  shift
done

[ -n "${version}" ] || {
  usage
  exit 1
}

if [[ ! "${version}" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
  die "版本号格式必须类似 1.2.3、v1.2.3 或 1.2.3-rc.1"
fi

tag="v${version#v}"
repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || die "当前目录不在 Git 仓库中"
cd "${repo_root}"

branch="$(git branch --show-current)"
[ -n "${branch}" ] || die "当前处于 detached HEAD，无法发布"

git remote get-url "${REMOTE}" >/dev/null || die "Git remote '${REMOTE}' 不存在"

if [ -n "$(git status --porcelain)" ]; then
  die "工作区不干净；发布 ${tag} 前请先提交或暂存当前改动"
fi

if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
  die "本地标签已存在：${tag}"
fi

remote_tag="$(git ls-remote --tags "${REMOTE}" "refs/tags/${tag}" 2>/dev/null)" || die "查询 ${REMOTE} 远端标签失败"
if [ -n "${remote_tag}" ]; then
  die "${REMOTE} 远端标签已存在：${tag}"
fi

if [ "${SKIP_TESTS}" != "1" ]; then
  command -v go >/dev/null || die "运行测试需要安装 go；如需跳过测试请使用 --skip-tests"
  run go test ./...
else
  echo "+ 跳过本地测试"
fi

created_tag=0
pushed_tag=0
cleanup_local_tag_on_error() {
  if [ "${created_tag}" = "1" ] && [ "${pushed_tag}" = "0" ] && [ "${DRY_RUN}" != "1" ]; then
    git tag -d "${tag}" >/dev/null 2>&1 || true
  fi
}
trap cleanup_local_tag_on_error ERR

run git tag -a "${tag}" -m "Release ${tag}"
created_tag=1

run git push "${REMOTE}" "HEAD:${branch}"
run git push "${REMOTE}" "refs/tags/${tag}:refs/tags/${tag}"
pushed_tag=1

if [ "${CREATE_GITHUB_RELEASE}" = "1" ]; then
  command -v gh >/dev/null || die "--create-github-release 需要安装 gh"
  run gh release create "${tag}" --title "${tag}" --notes "Backend release ${tag}" --verify-tag
fi

echo "已发布 ${tag} 到 ${REMOTE}。"
echo "GitHub Actions 会基于刚推送的标签自动构建并推送后端镜像。"
