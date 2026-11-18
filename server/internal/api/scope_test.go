package api

import (
	"reflect"
	"testing"

	"github.com/packrun/packrun/internal/model"
)

func TestScopeServerIDs(t *testing.T) {
	cases := []struct {
		name         string
		user         *model.User
		ids          []uint
		unrestricted bool
	}{
		{
			name:         "管理员忽略范围",
			user:         &model.User{Role: model.RoleAdmin, ServerScope: "2"},
			unrestricted: true,
		},
		{
			name:         "全部服务器通配",
			user:         &model.User{Role: model.RoleDeployer, ServerScope: "*"},
			unrestricted: true,
		},
		{
			name:         "空范围按全部处理",
			user:         &model.User{Role: model.RoleDeployer, ServerScope: ""},
			unrestricted: true,
		},
		{
			name:         "空白范围按全部处理",
			user:         &model.User{Role: model.RoleDeployer, ServerScope: "   "},
			unrestricted: true,
		},
		{
			name: "逗号分隔并忽略非法项",
			user: &model.User{Role: model.RoleDeployer, ServerScope: "2, 3, x, 0"},
			ids:  []uint{2, 3},
		},
		{
			name: "全部非法项",
			user: &model.User{Role: model.RoleDeployer, ServerScope: "abc,-1"},
		},
		{
			name: "空用户",
			user: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids, unrestricted := scopeServerIDs(tc.user)
			if unrestricted != tc.unrestricted {
				t.Fatalf("unrestricted = %v，期望 %v", unrestricted, tc.unrestricted)
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("ids = %v，期望 %v", ids, tc.ids)
			}
		})
	}
}

func TestCheckServerScope(t *testing.T) {
	admin := &model.User{Role: model.RoleAdmin, ServerScope: "99"}
	if !checkServerScope(admin, 1) {
		t.Fatal("管理员应可操作任意服务器")
	}

	deployer := &model.User{Role: model.RoleDeployer, ServerScope: "2,3"}
	if !checkServerScope(deployer, 2) || !checkServerScope(deployer, 3) {
		t.Fatal("范围内服务器应可操作")
	}
	if checkServerScope(deployer, 1) {
		t.Fatal("范围外服务器不应可操作")
	}

	empty := &model.User{Role: model.RoleDeployer, ServerScope: "x"}
	if checkServerScope(empty, 1) {
		t.Fatal("无有效范围时不应可操作")
	}
}
