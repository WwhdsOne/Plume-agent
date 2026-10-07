#!/bin/sh
# 构建带版本信息的 plume 二进制。
#
# 版本号来自仓库根的 VERSION 文件，commit 来自 git，构建时间取当前 UTC 时间。
# 三者通过 -ldflags -X 注入 cmd/plume，`plume version` 会如实打印出来。
#
# 用法：
#   scripts/build.sh                    构建 ./plume（默认剥掉符号表）
#   scripts/build.sh -o /tmp/plume     指定输出路径
#   scripts/build.sh --install          安装到 GOBIN（$GOBIN/bin 通常在 PATH 上）
#   scripts/build.sh --debug            保留符号表，便于看 panic 栈
#   scripts/build.sh -h                 显示帮助
#
# 刻意不做的事：不跑测试、不改 go.mod、不自动打 git tag。构建只管构建。

set -eu

usage() {
	cat <<'EOF'
用法: scripts/build.sh [选项]

  -o, --output PATH   输出路径（默认 ./plume）
      --install       改为 go install 到 GOBIN，不产出本地文件
      --debug         保留符号表与 DWARF（默认加 -s -w 剥离）
  -h, --help          显示本帮助

环境变量:
  VERSION_FILE        版本号文件路径，默认 <仓库根>/VERSION
EOF
}

output="plume"
install=0
strip=1

while [ $# -gt 0 ]; do
	case "$1" in
	-o | --output)
		[ $# -ge 2 ] || { echo "build: --output 需要一个路径" >&2; exit 2; }
		output="$2"
		shift 2
		;;
	--install)
		install=1
		shift
		;;
	--debug)
		strip=0
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "build: 未知参数 $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

# 切到仓库根，让脚本在任意目录下都能跑。
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(dirname -- "$script_dir")
cd "$repo_root"

# --- 版本号：VERSION 文件；缺失或为空时退回 dev，不假装有版本 ---
version_file="${VERSION_FILE:-$repo_root/VERSION}"
if [ -f "$version_file" ]; then
	version=$(tr -d '[:space:]' <"$version_file")
else
	version=""
fi
if [ -z "$version" ]; then
	version="dev"
	echo "build: 未找到 $version_file，版本号记为 dev" >&2
fi

# --- commit：短哈希；工作区有改动（含未跟踪文件）时加 -dirty ---
if commit=$(git rev-parse --short HEAD 2>/dev/null); then
	if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
		commit="$commit-dirty"
	fi
else
	commit="none"
	echo "build: 不是 git 仓库或没有提交，commit 记为 none" >&2
fi

build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)

ldflags="-X main.version=$version -X main.commit=$commit -X main.buildTime=$build_time"
if [ "$strip" -eq 1 ]; then
	ldflags="-s -w $ldflags"
fi

echo "version  $version"
echo "commit   $commit"
echo "built    $build_time"
echo "flags    $ldflags"

if [ "$install" -eq 1 ]; then
	echo "run      go install -ldflags \"$ldflags\" ./cmd/plume"
	go install -ldflags "$ldflags" ./cmd/plume
	bin=$(go env GOBIN)
	[ -n "$bin" ] || bin="$(go env GOPATH)/bin"
	echo "installed $bin/plume"
	echo
	"$bin/plume" version
else
	echo "run      go build -ldflags \"$ldflags\" -o $output ./cmd/plume"
	go build -ldflags "$ldflags" -o "$output" ./cmd/plume
	echo "built    $output"
	echo
	# 绝对路径直接用；相对路径补 ./，避免 shell 在 PATH 里找。
	case "$output" in
	/*) "$output" version ;;
	*) "./$output" version ;;
	esac
fi
