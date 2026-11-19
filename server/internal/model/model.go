package model

import (
	"time"

	"gorm.io/gorm"
)

// 角色
const (
	RoleAdmin    = "admin"    // 平台管理员：全部权限
	RoleDeployer = "deployer" // 发布执行者：可发起部署
	RoleAuditor  = "auditor"  // 审计只读：只可查看
)

// 服务器环境
const (
	EnvDev  = "dev"
	EnvTest = "test"
	EnvProd = "prod"
)

// 任务状态机
const (
	TaskPending      = "pending"
	TaskUploading    = "uploading"
	TaskVerifying    = "verifying"
	TaskExecuting    = "executing"
	TaskInterrupted  = "interrupted" // 服务重启中断，待人工恢复或标记失败
	TaskSuccess      = "success"
	TaskFailed       = "failed"
	TaskCanceled     = "canceled"
)

// 任务执行计划快照的归属阶段。
const (
	ScriptPhasePre  = "precheck"
	ScriptPhasePost = "postdeploy"
)

// 任务类型：常规发布 / 历史回滚
const (
	TaskKindDeploy   = "deploy"
	TaskKindRollback = "rollback"
)

// 脚本阶段
const (
	PhasePreCheck   = "precheck"   // 部署前校验
	PhasePostDeploy = "postdeploy" // 部署后执行
)

// 部署目录的包处理方式（仅对 zip 生效）
const (
	PackageModeUnpack = "unpack" // 平台自动解压到目标目录
	PackageModeUpload = "upload" // 仅上传压缩包，由部署后脚本处理
)

type User struct {
	ID               uint       `gorm:"primarykey" json:"id"`
	Username         string     `gorm:"uniqueIndex;size:64;not null" json:"username"`
	DisplayName      string     `gorm:"size:64" json:"displayName"`
	PasswordHash     string     `gorm:"size:120;not null" json:"-"`
	Role             string     `gorm:"size:24;not null;default:deployer" json:"role"`
	OTPSecret        string     `gorm:"size:64" json:"-"`
	PendingOTPSecret string     `gorm:"size:64" json:"-"`
	PendingOTPAt     *time.Time `json:"-"`
	OTPEnabled       bool       `gorm:"default:false" json:"otpEnabled"`
	ServerScope      string     `gorm:"size:255;default:'*'" json:"serverScope"` // "*" 或逗号分隔的服务器 ID
	ParentID         *uint      `gorm:"index" json:"parentId"`                   // 归属主账号；空表示自身为主账号
	Status           string     `gorm:"size:16;default:active" json:"status"`
	LastLoginAt      *time.Time `json:"lastLoginAt"`
	TokenVersion     int        `gorm:"default:1" json:"-"`
	LastOTPStep      int64      `gorm:"default:0" json:"-"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	// DeletedAt 软删除：保留任务与审计对操作人的引用，同时让会话立即失效。
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// RecoveryCode 是一次性登录恢复码，只保存哈希，明文仅在生成时返回一次。
type RecoveryCode struct {
	ID        uint       `gorm:"primarykey" json:"id"`
	UserID    uint       `gorm:"index;not null" json:"userId"`
	CodeHash  string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	UsedAt    *time.Time `json:"usedAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

type Server struct {
	ID            uint   `gorm:"primarykey" json:"id"`
	Name          string `gorm:"uniqueIndex;size:64;not null" json:"name"`
	Env           string `gorm:"size:16;not null;default:dev" json:"env"`
	Host          string `gorm:"size:128;not null" json:"host"`
	Port          int    `gorm:"default:22" json:"port"`
	Username      string `gorm:"size:64" json:"username"`
	AuthType      string `gorm:"size:16;default:key" json:"authType"` // key | password
	CredentialRef string `gorm:"size:255" json:"credentialRef"`       // env:XXX / file:/path / secret:内置凭据（明文绝不落库，直接录入走 SecretCipher 加密存储）
	// SecretCipher 「直接录入」凭据的 AES-256-GCM 密文（v1:nonce:ct），主密钥来自配置不落库；
	// 仅当 CredentialRef 为 secret: 前缀时有值，且永不序列化到 API 响应。
	SecretCipher  string `gorm:"type:text" json:"-"`
	Status        string `gorm:"size:16;default:unknown" json:"status"`
	MaxConcurrent int    `gorm:"default:1" json:"maxConcurrent"`
	Remark        string `gorm:"size:255" json:"remark"`

	// 主机指纹（TOFU 首次信任）：首次 SSH 握手自动登记，之后每次连接据此校验，
	// 指纹不匹配视为服务器重建或中间人风险。公钥本身为公开信息。
	HostKeyAlgo        string     `gorm:"size:32" json:"hostKeyAlgo"`        // 如 ssh-ed25519
	HostKey            string     `gorm:"type:text" json:"-"`                // 公钥 base64，不参与序列化
	HostKeyFingerprint string     `gorm:"size:64" json:"hostKeyFingerprint"` // SHA256:...
	HostKeyPinnedAt    *time.Time `json:"hostKeyPinnedAt"`

	// Metrics 最新一次运行指标，非持久化字段，由查询侧填充。
	Metrics *ServerMetric `gorm:"-" json:"metrics,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DirUsage 部署目录所在文件系统的占用快照。
type DirUsage struct {
	Path    string  `json:"path"`
	TotalMB int     `json:"totalMB"`
	UsedMB  int     `json:"usedMB"`
	Pct     float64 `json:"pct"`
}

// ServerMetric 服务器最近一次运行指标快照，每台服务器最多一条（server_id 唯一），
// 由后台定时采集与手动检测写入。
type ServerMetric struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	ServerID   uint      `gorm:"uniqueIndex;not null" json:"serverId"`
	Status     string    `gorm:"size:16" json:"status"` // online | offline
	Message    string    `gorm:"size:512" json:"message"`
	Hostname   string    `gorm:"size:128" json:"hostname"`
	OS         string    `gorm:"size:128" json:"os"`
	UptimeSec  int64     `json:"uptimeSec"`
	CPUCores   int       `json:"cpuCores"`
	Load1      float64   `json:"load1"`
	Load5      float64   `json:"load5"`
	Load15     float64   `json:"load15"`
	MemTotalMB int       `json:"memTotalMB"`
	MemUsedMB  int       `json:"memUsedMB"`
	DirUsage   string    `gorm:"type:text" json:"dirUsage"` // JSON 数组：[]DirUsage
	ProbeMode  string    `gorm:"size:16" json:"probeMode"`  // ssh | local
	ProbedAt   time.Time `json:"probedAt"`
}

type Directory struct {
	ID                 uint      `gorm:"primarykey" json:"id"`
	ServerID           uint      `gorm:"index;not null" json:"serverId"`
	Path               string    `gorm:"size:512;not null" json:"path"`
	Owner              string    `gorm:"size:64" json:"owner"`
	AllowOverwrite     bool      `json:"allowOverwrite"`
	BackupBeforeDeploy bool      `json:"backupBeforeDeploy"`
	CleanReplace       bool      `json:"cleanReplace"` // 干净替换：zip 解包时清理包内不存在的旧文件（仅限部署目录内）
	PackageMode        string    `gorm:"size:16;default:unpack" json:"packageMode"` // unpack | upload，仅对 zip 生效
	KeepReleases       int       `gorm:"default:3" json:"keepReleases"`
	MaxSizeMB          int       `gorm:"default:1024" json:"maxSizeMB"`
	Remark             string    `gorm:"size:255" json:"remark"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type Script struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	Name        string    `gorm:"size:64;not null" json:"name"`
	Phase       string    `gorm:"size:24;not null;default:postdeploy" json:"phase"`
	Content     string    `gorm:"type:text" json:"content"`
	TimeoutSec  int       `gorm:"default:120" json:"timeoutSec"`
	RunAs       string    `gorm:"size:64" json:"runAs"`
	Version     int       `gorm:"default:1" json:"version"`
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Project 工作项目：以项目为单位组织发布目标、成员与发布历史。
type Project struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	Name        string    `gorm:"size:64;not null" json:"name"`
	Code        string    `gorm:"size:64;not null" json:"code"`
	Description string    `gorm:"size:255" json:"description"`
	OwnerID     uint      `gorm:"index;not null" json:"ownerId"`
	ScriptID    *uint     `json:"scriptId"`                              // 项目默认部署后脚本（postdeploy），发布时未单独指定则沿用
	Version     string    `gorm:"size:24;default:v0.0.0" json:"version"` // 项目当前版本号，随发布批次推进
	Status      string    `gorm:"size:16;default:active" json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ProjectDirectory 项目 ↔ 更新目录 绑定关系。
type ProjectDirectory struct {
	ProjectID   uint `gorm:"primarykey;autoIncrement:false" json:"projectId"`
	DirectoryID uint `gorm:"primarykey;autoIncrement:false" json:"directoryId"`
}

// ProjectMember 项目成员授权：子用户仅可见被授权的项目。
type ProjectMember struct {
	ProjectID uint `gorm:"primarykey;autoIncrement:false" json:"projectId"`
	UserID    uint `gorm:"primarykey;autoIncrement:false" json:"userId"`
}

type Package struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	Name       string    `gorm:"size:255;not null" json:"name"`
	Kind       string    `gorm:"size:16;not null" json:"kind"` // zip | jar
	Size       int64     `json:"size"`
	SHA256     string    `gorm:"size:64;index" json:"sha256"`
	StorageKey string    `gorm:"size:255" json:"-"`
	UploaderID uint      `json:"uploaderId"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Task struct {
	ID               uint       `gorm:"primarykey" json:"id"`
	TaskNo           string     `gorm:"uniqueIndex;size:40;not null" json:"taskNo"`
	ServerID         uint       `gorm:"index" json:"serverId"`
	ServerName       string     `gorm:"size:64" json:"serverName"`
	DirectoryID      uint       `json:"directoryId"`
	TargetPath       string     `gorm:"size:512" json:"targetPath"`
	PackageID        uint       `json:"packageId"`
	PackageName      string     `gorm:"size:255" json:"packageName"`
	PreScriptID      *uint      `json:"preScriptId"`
	PreScriptName    string     `gorm:"size:64" json:"preScriptName"`
	PreScriptVersion int        `json:"preScriptVersion"`
	ScriptID         *uint      `json:"scriptId"`
	ScriptName       string     `gorm:"size:64" json:"scriptName"`
	ScriptVersion    int        `json:"scriptVersion"`
	ProjectID        uint       `gorm:"index" json:"projectId"`       // 归属工作项目；0 表示部署向导发起
	BatchNo          string     `gorm:"size:40;index" json:"batchNo"` // 项目发布批次号：同批次 = 一次历史记录
	Kind             string     `gorm:"size:16;default:deploy" json:"kind"`
	ProjectVersion   string     `gorm:"size:24" json:"projectVersion"` // 该批次对应的项目版本
	Remark           string     `gorm:"size:255" json:"remark"`        // 如：回滚自 vX.Y.Z
	OperatorID       uint       `json:"operatorId"`
	Operator         string     `gorm:"size:64" json:"operator"`
	Status           string     `gorm:"size:24;index" json:"status"`
	Stage            string     `gorm:"size:64" json:"stage"`
	Progress         int        `json:"progress"`
	Message          string     `gorm:"size:512" json:"message"`
	OTPVerified      bool       `json:"otpVerified"`
	// Manifest zip 发布的文件级差异清单 JSON（结构见 deploy.Manifest），非 zip 发布为空。
	Manifest    string     `gorm:"type:text" json:"manifest"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// TaskScript 任务受理时的脚本快照：执行与展示均以快照为准，
// 脚本后续编辑、删除都不影响已受理任务（收口手册 §21.7）。
type TaskScript struct {
	ID         uint   `gorm:"primarykey" json:"id"`
	TaskID     uint   `gorm:"index;not null" json:"taskId"`
	Phase      string `gorm:"size:24;not null" json:"phase"` // precheck | postdeploy
	ScriptID   uint   `json:"scriptId"`
	Name       string `gorm:"size:64" json:"name"`
	Version    int    `json:"version"`
	Content    string `gorm:"type:text" json:"content"`
	TimeoutSec int    `json:"timeoutSec"`
	RunAs      string `gorm:"size:64" json:"runAs"`
}

// AlertDedup 告警去重状态：跨重启保证同一事件在窗口内只推送一次（收口手册 §21.13）。
type AlertDedup struct {
	Key    string    `gorm:"primarykey;size:128" json:"key"`
	LastAt time.Time `json:"lastAt"`
}

type TaskStep struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	TaskID     uint       `gorm:"index" json:"taskId"`
	Seq        int        `json:"seq"`
	Name       string     `gorm:"size:64" json:"name"`
	Status     string     `gorm:"size:16" json:"status"` // running | success | failed | skipped
	Output     string     `gorm:"type:text" json:"output"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

type AuditLog struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	ActorID     uint      `gorm:"index" json:"actorId"`
	Actor       string    `gorm:"size:64" json:"actor"`
	Action      string    `gorm:"size:64;index" json:"action"`
	Target      string    `gorm:"size:255" json:"target"`
	Result      string    `gorm:"size:16" json:"result"` // ok | fail
	IP          string    `gorm:"size:64" json:"ip"`
	TaskNo      string    `gorm:"size:40;index" json:"taskNo"`
	OTPVerified bool      `json:"otpVerified"`
	Detail      string    `gorm:"type:text" json:"detail"`
	CreatedAt   time.Time `json:"createdAt"`
}
