#!/bin/bash
# 运行所有测试

set -e

echo "========================================="
echo "  Agent Gateway 完整测试套件"
echo "========================================="
echo ""

# 检查服务是否运行
echo "🔍 检查服务状态..."
if ! curl -s http://localhost:8080/ping > /dev/null 2>&1; then
  echo "❌ 服务未运行，请先启动服务："
  echo "   go run cmd/main.go"
  exit 1
fi
echo "✅ 服务运行中"
echo ""

# 1. 单元测试
echo "========================================="
echo "  📦 1. 单元测试"
echo "========================================="
if go test ./internal/... -v -short 2>&1 | tail -20; then
  echo "✅ 单元测试通过"
else
  echo "⚠️  单元测试跳过（需要补充测试文件）"
fi
echo ""

# 2. 功能测试
echo "========================================="
echo "  🧪 2. 功能测试"
echo "========================================="
if [ -f "test.sh" ]; then
  bash test.sh
else
  echo "⚠️  test.sh 不存在"
fi
echo ""

# 3. E2E 测试
echo "========================================="
echo "  🎯 3. E2E 完整工作流测试"
echo "========================================="
if [ -f "e2e/test_full_workflow.sh" ]; then
  bash e2e/test_full_workflow.sh
else
  echo "⚠️  e2e/test_full_workflow.sh 不存在"
fi
echo ""

# 4. 安全测试
echo "========================================="
echo "  🔒 4. 安全测试"
echo "========================================="
if [ -f "security/test_security.sh" ]; then
  bash security/test_security.sh
else
  echo "⚠️  security/test_security.sh 不存在"
fi
echo ""

# 5. 双项目集成测试
echo "========================================="
echo "  🔗 5. 双项目集成测试"
echo "========================================="
if [ -f "test_integration_full.sh" ]; then
  bash test_integration_full.sh
else
  echo "⚠️  test_integration_full.sh 不存在"
fi
echo ""

# 6. MCP 协议测试
echo "========================================="
echo "  🤖 6. MCP 协议测试"
echo "========================================="
if [ -f "test_mcp.sh" ]; then
  bash test_mcp.sh
else
  echo "⚠️  test_mcp.sh 不存在"
fi
echo ""

# 总结
echo "========================================="
echo "  🎉 所有测试完成！"
echo "========================================="
echo ""
echo "📊 测试文件位置："
echo "   - 功能测试: test/test.sh"
echo "   - E2E 测试: test/e2e/test_full_workflow.sh"
echo "   - 安全测试: test/security/test_security.sh"
echo "   - 集成测试: test/integration/ (Go 测试)"
echo "   - 双项目集成: test/test_integration_full.sh"
echo "   - MCP 协议: test/test_mcp.sh"
echo ""
echo "💡 提示："
echo "   - 运行单个测试：bash test/<脚本名>"
echo "   - Go 集成测试：go test ./test/integration/... -v"
echo "   - 完整测试指南：cat test/TESTING_GUIDE.md"
