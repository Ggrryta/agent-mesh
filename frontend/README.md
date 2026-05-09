# Agent Gateway 前端页面

## 页面列表

| 页面 | 文件 | 说明 |
|------|------|------|
| 首页 | [index.html](index.html) | 系统概览和导航入口 |
| 注册/登录 | [auth.html](auth.html) | 客户自助注册账号、登录获取 JWT Token |
| Skill 管理 | [skills.html](skills.html) | 注册、查看、编辑、删除 Skill |
| 权限申请 | [apply.html](apply.html) | 申请私有 Skill 调用权限，审批他人的申请 |
| 调用测试台 | [invoke.html](invoke.html) | 选择 Skill，填写 input，发起同步/异步/流式调用 |
| 实时通知 | [notifications.html](notifications.html) | SSE 长连接，实时接收申请提交和审批结果通知 |

## 快速开始

### 方式一：Docker Compose（推荐）

```bash
# 一键启动所有服务（MySQL + Redis + Agent Gateway）
./docker-start.sh

# 访问前端页面
open http://localhost:8080
```

### 方式二：本地启动

```bash
# 1. 确保 MySQL 和 Redis 已启动

# 2. 运行数据库迁移
go run ./cmd/migrate/main.go

# 3. 启动服务
./start.sh

# 或使用 Docker Compose
docker-compose up -d
```

## 使用流程

1. **注册账号**：访问 http://localhost:8080/auth.html，填写 App ID 和密码
2. **登录获取 Token**：使用注册的账号登录，Token 自动保存到浏览器
3. **注册 Skill**：访问 Skill 管理页面，注册你的能力
4. **调用测试**：在调用测试台选择 Skill，填写 input，发起调用
5. **权限管理**：如果是私有 Skill，其他用户需要申请权限，你在收件箱审批

## 技术栈

- 纯前端 HTML + CSS + JavaScript（无框架依赖）
- 使用 Fetch API 调用后端接口
- SSE (Server-Sent Events) 实现实时通知
- LocalStorage 保存认证信息

## API 配置

前端 API 地址配置在 [common.js](common.js) 中：

```javascript
const API_BASE = 'http://localhost:8080'
```

如果后端服务运行在其他地址，需要修改此配置。

## 功能特性

✅ 响应式设计，支持移动端  
✅ Token 自动管理，登录后自动携带  
✅ JSON 语法高亮显示  
✅ Toast 提示反馈  
✅ 实时通知推送  
✅ 跨域支持（CORS）
