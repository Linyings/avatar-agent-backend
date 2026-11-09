// agent_runner.go — 把 Agent 的制作计划接到真实工作流引擎
//
// agent 包只声明 ProjectRunner 接口，具体怎么执行由这里决定：
// 复用 avatarWorkflowService 既有的 create + launch，不重写执行逻辑。
// 归属校验直接查项目行（owner_id），因为工具层只传 projectID。
package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

// agentProjectRunner 实现 agent.ProjectRunner
type agentProjectRunner struct {
	app *App
}

// Run 提交一次工作流执行。
//
// 语义说明：
//   - target 是希望执行到的节点（F01.4/F02/.../F12）。现有执行引擎从当前状态
//     顺序推进到目标节点，因此这里只需提交一次运行，由引擎自己按依赖执行。
//   - 必须在提交后立即返回：工作流可能跑几分钟，不能占用 Agent 的 HTTP 请求。
//   - 拒绝原因（节点前置条件不满足、缺照片、额度不足等）作为 error 返回，
//     由 Agent 如实转述给用户。
func (r agentProjectRunner) Run(ctx context.Context, projectID, target string) error {
	projectUUID, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return fmt.Errorf("项目 ID 无效")
	}

	project, in, err := r.ownerProject(ctx, projectUUID)
	if err != nil {
		return err
	}

	// 目标节点必须存在，否则说明模型编造了节点名
	if target != "" && !isKnownWorkflowNode(target) {
		return fmt.Errorf("%s 不是有效的制作节点（可用节点：F01.4、F02–F12）", target)
	}

	// 复用与 HTTP 入口一致的校验，避免 Agent 绕过前置条件
	if err := r.app.avatarWorkflow.validateInput(&in, true); err != nil {
		return err
	}

	// revision 用当前项目输入版本，防止基于过期输入执行
	requestID := uuid.NewString()
	state, err := r.app.avatarWorkflow.create(ctx, project.OwnerID, projectUUID, requestID, in.Revision)
	if err != nil {
		return err
	}
	if state.Status == "running" {
		r.app.avatarWorkflow.launch(state, project.OwnerID)
	}
	return nil
}

// ownerProject 取项目行和它的工作流入参。
// 这里不做「是不是当前登录用户」的判断——调用链上游 handler 已经 authorizeProject 过，
// 此处只负责把 projectID 解析成 owner，供 create 做归属复核。
func (r agentProjectRunner) ownerProject(ctx context.Context, id uuid.UUID) (models.Project, avatar.WorkflowInput, error) {
	var p models.Project
	if err := r.app.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		return p, avatar.WorkflowInput{}, fmt.Errorf("项目不存在")
	}
	in, err := workflowInput(p)
	if err != nil {
		return p, avatar.WorkflowInput{}, fmt.Errorf("项目缺少制作参数，请先在首页描述需求")
	}
	return p, in, nil
}

// isKnownWorkflowNode 校验节点名是否属于工作流定义
func isKnownWorkflowNode(node string) bool {
	for _, n := range avatar.WorkflowNodes {
		if n == node {
			return true
		}
	}
	return false
}

// 编译期断言：确保实现满足 agent 侧契约
var _ agent.ProjectRunner = agentProjectRunner{}
