package deploy

import (
	"context"

	"github.com/packrun/packrun/internal/model"
)

// Pinger 可选能力：探测目标服务器连通性。
type Pinger interface {
	Ping(ctx context.Context, srv model.Server) error
}

// Ping 本地沙箱模式恒为可达。
func (d *LocalDeployer) Ping(ctx context.Context, srv model.Server) error {
	return nil
}

// Ping SSH 模式实际建连后立即关闭，验证网络与凭据。
func (d *SSHDeployer) Ping(ctx context.Context, srv model.Server) error {
	client, err := d.dial(srv)
	if err != nil {
		return err
	}
	return client.Close()
}

var (
	_ Pinger = (*LocalDeployer)(nil)
	_ Pinger = (*SSHDeployer)(nil)
)
