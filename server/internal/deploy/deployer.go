package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/packrun/packrun/internal/model"
)

// 步骤名称：任务详情页按此顺序渲染时间线。
const (
	StepPrepare  = "准备部署目录"
	StepPreCheck = "执行前置校验"
	StepBackup   = "备份现有版本"
	StepUpload   = "上传安装包"
	StepVerify   = "校验安装包"
	StepUnpack   = "解包与替换"
	StepScript   = "执行部署脚本"
)

// Hooks 用于把执行过程中的阶段、日志回传给任务记录。
type Hooks struct {
	OnStep  func(name string)
	OnLog   func(line string)
	OnStage func(stage string, progress int)
}

func (h Hooks) step(name string) {
	if h.OnStep != nil {
		h.OnStep(name)
	}
}

func (h Hooks) logf(line string) {
	if h.OnLog != nil && line != "" {
		h.OnLog(line)
	}
}

func (h Hooks) stage(name string, pct int) {
	if h.OnStage != nil {
		h.OnStage(name, pct)
	}
}

// Request 是一次部署所需的全部输入。
type Request struct {
	Server     model.Server
	Directory  model.Directory
	Package    model.Package
	PreScript  *model.Script // 前置校验脚本，解包前执行
	Script     *model.Script // 后置脚本，解包后执行
	SourcePath string        // 中控机上的包文件绝对路径
	Hooks      Hooks
}

// Deployer 抽象执行通道：SSH 与未来内部 RPC 均实现此接口。
type Deployer interface {
	Deploy(ctx context.Context, req Request) error
}

// serverNamePattern 限制服务器名只能作为安全的目录片段使用。
var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidateServerName 校验服务器名称：仅允许字母、数字、点、下划线与连字符，且拒绝 . 和 ..。
func ValidateServerName(name string) error {
	if !serverNamePattern.MatchString(name) || name == "." || name == ".." {
		return errors.New("服务器名称仅支持字母、数字、点、下划线与连字符")
	}
	return nil
}

// NormalizePath 校验部署目录：必须绝对路径，禁止路径穿越。
func NormalizePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("部署目录不能为空")
	}
	if !strings.HasPrefix(p, "/") {
		return "", errors.New("部署目录必须是绝对路径")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", errors.New("部署目录不允许包含 ..")
		}
	}
	clean := filepath.Clean(p)
	if strings.Contains(clean, "..") {
		return "", errors.New("部署目录不允许包含 ..")
	}
	if clean == "/" {
		return "", errors.New("不允许以根目录作为部署目录")
	}
	return clean, nil
}
