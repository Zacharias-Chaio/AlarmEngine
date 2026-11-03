package config

import "testing"

func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*App)
		wantOK bool
	}{
		{"默认配置合法", func(*App) {}, true},
		{"NATS 启用但缺地址", func(a *App) { a.Nats.Enabled = true; a.Nats.URL = "" }, false},
		{"NATS 主题含空白", func(a *App) { a.Nats.Enabled = true; a.Nats.AlarmSubject = "powerpulse alarm" }, false},
		{"物模型前缀含通配符", func(a *App) { a.Nats.Enabled = true; a.Nats.ThingsmodelPrefix = "powerpulse.*" }, false},
		{"保留天数为负", func(a *App) { a.Retention.Days = -1 }, false},
		{"REQ 超时为负", func(a *App) { a.Nats.Enabled = true; a.Nats.RequestTimeout = -1 }, false},
		{"NATS 关闭时数值配置仍校验", func(a *App) { a.Nats.Enabled = false; a.Nats.ConnectTimeout = -1 }, false},
	}
	for _, testCase := range cases {
		app := Default()
		testCase.mutate(&app)
		err := Validate(app)
		if testCase.wantOK && err != nil {
			t.Fatalf("%s: 期望通过，实际 %v", testCase.name, err)
		}
		if !testCase.wantOK && err == nil {
			t.Fatalf("%s: 期望失败，实际通过", testCase.name)
		}
	}
}
