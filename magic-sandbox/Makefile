# =============================================================================
# Magic Sandbox Chart - Helm Chart 调试和管理工具
# =============================================================================

# 项目配置
CHART_NAME := magic-sandbox

# 颜色配置
GREEN := \033[32m
YELLOW := \033[33m
BLUE := \033[34m
NC := \033[0m # No Color

.PHONY: help
help: ## 显示帮助信息
	@echo "$(BLUE)$(CHART_NAME) Chart - Helm 调试工具$(NC)"
	@echo "$(BLUE)==============================$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "$(GREEN)%-20s$(NC) %s\n", $$1, $$2}'

# =============================================================================
# 🔧 基础操作
# =============================================================================

.PHONY: clean
clean: ## 清理所有生成的文件和依赖
	@echo "$(YELLOW)清理 $(CHART_NAME) Chart 文件...$(NC)"
	rm -rf charts/*.tgz
	rm -rf Chart.lock
	@echo "$(GREEN)清理完成!$(NC)"

.PHONY: deps
deps: ## 构建所有依赖
	@echo "$(YELLOW)构建 $(CHART_NAME) 依赖...$(NC)"
	@if ! helm dependency build .; then \
		echo "$(YELLOW)依赖构建失败，尝试更新依赖...$(NC)"; \
		helm dependency update; \
	fi
	@echo "$(GREEN)$(CHART_NAME) 依赖构建完成!$(NC)"

.PHONY: lint
lint: deps ## 检查 Chart 语法
	@echo "$(YELLOW)检查 $(CHART_NAME) Chart 语法...$(NC)"
	helm lint .
	@echo "$(GREEN)语法检查完成!$(NC)"

# =============================================================================
# 🔍 调试和验证
# =============================================================================

.PHONY: template
template: deps ## 渲染模板并输出到文件
	@echo "$(YELLOW)渲染 $(CHART_NAME) Helm 模板...$(NC)"
	mkdir -p output
	helm template $(CHART_NAME) . --output-dir ./output
	@echo "$(GREEN)模板已渲染到 ./output 目录$(NC)"

.PHONY: template-debug
template-debug: deps ## 渲染模板并显示调试信息
	@echo "$(YELLOW)渲染 $(CHART_NAME) 模板（调试模式）...$(NC)"
	helm template $(CHART_NAME) . --debug

.PHONY: values
values: ## 显示所有计算后的 values
	@echo "$(YELLOW)显示 $(CHART_NAME) 计算后的 values...$(NC)"
	helm show values .

# =============================================================================
# 📋 信息显示
# =============================================================================

.PHONY: info
info: ## 显示项目信息
	@echo "$(BLUE)$(CHART_NAME) Chart 信息$(NC)"
	@echo "$(BLUE)=================$(NC)"
	@echo "$(GREEN)Chart名称:$(NC) $(CHART_NAME)"
	@echo ""
	@echo "$(BLUE)子Chart结构:$(NC)"
	@find . -name "Chart.yaml" -exec dirname {} \; | sort

.PHONY: env
env: ## 显示环境信息
	@echo "$(BLUE)环境信息$(NC)"
	@echo "$(BLUE)========$(NC)"
	@echo "$(GREEN)Helm 版本:$(NC)"
	@helm version --short
	@echo ""
	@echo "$(GREEN)Kubectl 版本:$(NC)"
	@kubectl version --short 2>/dev/null || echo "kubectl 未连接到集群"
	@echo ""
	@echo "$(GREEN)当前上下文:$(NC)"
	@kubectl config current-context 2>/dev/null || echo "无可用上下文"

# 默认目标
.DEFAULT_GOAL := help
