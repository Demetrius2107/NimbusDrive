#!/usr/bin/env bash
# 从 master..当前分支 的提交生成 PR 描述（填好模板，非空壳）。
# 用法：bash scripts/gen-pr-body.sh > pr_body.md
#       然后复制内容粘贴到 GitHub PR 描述框。
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

BRANCH=$(git branch --show-current)
BASE="${1:-master}"

echo "<!-- 由 scripts/gen-pr-body.sh 自动生成，基于 ${BASE}..${BRANCH} 的提交。请按需精简。 -->"
echo ""
echo "## 变更摘要"
echo ""

# 从 commit message 第一行提取主题摘要。
git log "${BASE}..HEAD" --pretty=format:"%s" | while IFS= read -r line; do
  echo "- ${line}"
done
echo ""

echo "## 动机"
echo ""
echo "<!-- 为什么做这个改动？关联 issue / 设计文档章节 -->"
echo ""

echo "## 变更内容"
echo ""
echo "<!-- 按提交粒度展开，自动从 commit message 提取 -->"
echo ""

# 提取每个 commit 的标题 + body，格式化成 PR 内容。
git log "${BASE}..HEAD" --reverse --pretty=format:"@@%s%n%b" | awk '
  /^@@/ {
    if (title != "") {
      print "### " title
      print ""
      if (body != "") {
        print body
        print ""
      }
    }
    title = substr($0, 3)
    body = ""
    next
  }
  /^$/ { next }
  {
    if (body == "") body = $0
    else body = body "\n" $0
  }
  END {
    if (title != "") {
      print "### " title
      print ""
      if (body != "") {
        print body
        print ""
      }
    }
  }
'

echo "## 涉及文件"
echo ""
echo ""
git diff --stat "${BASE}..HEAD" | tail -1
echo ""

echo "## 测试"
echo ""
echo "- [ ] \`GOARCH=amd64 go vet ./...\` 通过"
echo "- [ ] \`GOARCH=amd64 go build ./...\` 通过"
echo "- [ ] 关键链路手测通过（描述步骤）："
echo '  ```'
echo "  1. ..."
echo "  2. ..."
echo '  ```'
echo "- [ ] 前端 \`tsc --noEmit\` 通过（如涉及）"
echo ""

echo "## 影响范围"
echo ""
echo "<!-- 是否影响现有接口/数据结构/部署？是否有破坏性变更？ -->"
echo ""

echo "## 检查清单"
echo ""
echo "- [ ] 提交信息符合 Conventional Commits（\`docs/开发规范.md\`）"
echo "- [ ] 一个分支只做一件事"
echo "- [ ] 未提交敏感信息（密钥/密码/配置）"
echo "- [ ] 新增依赖已在 \`go.mod\` / \`package.json\` 更新"
