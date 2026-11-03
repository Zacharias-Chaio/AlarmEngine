package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"alarmengine/internal/config"
	"alarmengine/internal/natsclient"
	"alarmengine/internal/runtime"
	"alarmengine/internal/store"
)

func newRuleTestServer(t *testing.T) *Server {
	t.Helper()
	s := newAlarmTestServer(t)
	if err := s.DB.AutoMigrate(&store.AlarmRule{}, &store.ActionRecord{}); err != nil {
		t.Fatalf("迁移规则表失败: %v", err)
	}
	return s
}

func postRule(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.SaveRule(rec, req)
	return rec
}

const validRuleBody = `{"id":"r1","version":0,"name":"规则A","enabled":true,"matchMode":"anyone","cooldownMs":5000,"monitors":[{"kind":"level","levels":[2,3]},{"kind":"event","deviceId":"bms-001","eventKey":"temp_high"}],"controls":[{"kind":"device","deviceId":"pcs-001","serviceKey":"setPower","value":50}]}`

func TestSaveRuleMultiSourceLifecycle(t *testing.T) {
	s := newRuleTestServer(t)

	// 1. 创建：多源落库。
	rec := postRule(t, s, validRuleBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建应成功: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AlarmRule
	if err := s.DB.First(&created, "id = ?", "r1").Error; err != nil {
		t.Fatalf("规则未落库: %v", err)
	}
	if len(created.Monitors) != 2 || created.Monitors[0].Kind != "level" ||
		len(created.Monitors[0].Levels) != 2 || created.Monitors[1].EventKey != "temp_high" {
		t.Fatalf("监测源落库错误: %+v", created.Monitors)
	}
	if len(created.Controls) != 1 || created.Controls[0].ServiceKey != "setPower" || created.Controls[0].Value != 50 {
		t.Fatalf("控制源落库错误: %+v", created.Controls)
	}
	if created.MatchMode != store.RuleMatchAny || created.Version != 0 {
		t.Fatalf("聚合模式/版本错误: %+v", created)
	}

	// 2. 以当前版本 0 保存更新 → 200，版本递增为 1。
	v1Body := strings.Replace(validRuleBody, `"version":0`, `"version":1`, 1)
	v1Body = strings.Replace(v1Body, "规则A", "规则A-v2", 1)
	// 注：第一次保存时客户端已把“当前版本=0”回填；此处先用版本 0 更新（合法）。
	sameVersion := strings.Replace(validRuleBody, "规则A", "规则A-v2", 1)
	if rec := postRule(t, s, sameVersion); rec.Code != http.StatusOK {
		t.Fatalf("当前版本保存应成功: %d %s", rec.Code, rec.Body.String())
	}
	var step1 store.AlarmRule
	s.DB.First(&step1, "id = ?", "r1")
	if step1.Version != 1 || step1.Name != "规则A-v2" {
		t.Fatalf("更新后字段错误: %+v", step1)
	}

	// 3. 此时数据库已是版本 1：再用旧版本 0 保存 → 409 冲突。
	if rec := postRule(t, s, sameVersion); rec.Code != http.StatusConflict {
		t.Fatalf("旧版本保存应 409: %d", rec.Code)
	}

	// 4. 用正确版本 1 保存 → 200，版本递增为 2。
	if rec := postRule(t, s, v1Body); rec.Code != http.StatusOK {
		t.Fatalf("新版本保存应成功: %d %s", rec.Code, rec.Body.String())
	}
	var reloaded store.AlarmRule
	s.DB.First(&reloaded, "id = ?", "r1")
	if reloaded.Version != 2 || reloaded.Name != "规则A-v2" {
		t.Fatalf("更新后字段错误: %+v", reloaded)
	}
}

func TestSaveRuleValidation(t *testing.T) {
	s := newRuleTestServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"无监测源", `{"id":"b1","version":0,"name":"x","monitors":[],"controls":[{"kind":"device","deviceId":"d","serviceKey":"s","value":1}]}`},
		{"级别为空", `{"id":"b2","version":0,"name":"x","monitors":[{"kind":"level","levels":[]}],"controls":[{"kind":"device","deviceId":"d","serviceKey":"s","value":1}]}`},
		{"级别越界", `{"id":"b3","version":0,"name":"x","monitors":[{"kind":"level","levels":[4]}],"controls":[{"kind":"device","deviceId":"d","serviceKey":"s","value":1}]}`},
		{"事件源缺字段", `{"id":"b4","version":0,"name":"x","monitors":[{"kind":"event","deviceId":""}],"controls":[{"kind":"device","deviceId":"d","serviceKey":"s","value":1}]}`},
		{"未知监测源", `{"id":"b5","version":0,"name":"x","monitors":[{"kind":"other"}],"controls":[{"kind":"device","deviceId":"d","serviceKey":"s","value":1}]}`},
		{"无控制源", `{"id":"b6","version":0,"name":"x","monitors":[{"kind":"level","levels":[3]}],"controls":[]}`},
		{"非设备通知", `{"id":"b7","version":0,"name":"x","monitors":[{"kind":"level","levels":[3]}],"controls":[{"kind":"sms","deviceId":"d","serviceKey":"s","value":1}]}`},
		{"聚合模式非法", `{"id":"b8","version":0,"name":"x","matchMode":"bad","monitors":[{"kind":"level","levels":[3]}],"controls":[{"kind":"device","deviceId":"d","serviceKey":"s","value":1}]}`},
	}
	for _, testCase := range cases {
		if rec := postRule(t, s, testCase.body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d (%s)", testCase.name, rec.Code, rec.Body.String())
		}
	}
}

// C1：新建"停用"规则必须原样落库为停用（GORM default tag 不得覆盖 false）。
// 同时覆盖 H4：value=0 必须完整回读（omitempty 不得省略归零指令）。
func TestSaveRuleDisabledCreate(t *testing.T) {
	s := newRuleTestServer(t)
	body := `{"id":"r-off","version":0,"name":"停用规则","enabled":false,"matchMode":"anyone","monitors":[{"kind":"level","levels":[3]}],"controls":[{"kind":"device","deviceId":"pcs-001","serviceKey":"setPower","value":0}]}`
	rec := postRule(t, s, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建应成功: %d %s", rec.Code, rec.Body.String())
	}
	var got store.AlarmRule
	if err := s.DB.First(&got, "id = ?", "r-off").Error; err != nil {
		t.Fatalf("规则未落库: %v", err)
	}
	if got.Enabled {
		t.Fatalf("停用规则被改写为启用: %+v", got)
	}
	var count int64
	s.DB.Model(&store.AlarmRule{}).Where("enabled = ?", true).Count(&count)
	if count != 0 {
		t.Fatalf("停用规则不应出现在启用集合")
	}
	// 列表接口回读：value=0 必须出现（前端编辑链路依赖它）。
	listRec := httptest.NewRecorder()
	s.ListRules(listRec, httptest.NewRequest(http.MethodGet, "/api/rules", nil))
	if !strings.Contains(listRec.Body.String(), `"value":0`) {
		t.Fatalf("value=0 在回读链路中被省略: %s", listRec.Body.String())
	}
}

// 事件源与级别源均按"源告警仍在活动"判定（DB 活性），all 模式允许多个事件源：
// 各自要求自己的源告警处于活动态，全部成立才触发。
func TestSaveRuleAllModeMultiEventAllowed(t *testing.T) {
	s := newRuleTestServer(t)
	body := `{"id":"r-all","version":0,"name":"多事件 all","enabled":true,"matchMode":"all",` +
		`"monitors":[{"kind":"event","deviceId":"bms-001","eventKey":"temp_high"},{"kind":"event","deviceId":"bms-001","eventKey":"volt_drop"}],` +
		`"controls":[{"kind":"device","deviceId":"pcs-001","serviceKey":"setPower","value":10}]}`
	if rec := postRule(t, s, body); rec.Code != http.StatusOK {
		t.Fatalf("all + 多事件源应允许保存（事件源按 DB 活性判定）: %d %s", rec.Code, rec.Body.String())
	}
	// 单事件源 + level 源的 all 组合同样允许。
	okBody := `{"id":"r-all-ok","version":0,"name":"level+event all","enabled":true,"matchMode":"all",` +
		`"monitors":[{"kind":"level","levels":[3]},{"kind":"event","deviceId":"bms-001","eventKey":"temp_high"}],` +
		`"controls":[{"kind":"device","deviceId":"pcs-001","serviceKey":"setPower","value":10}]}`
	if rec := postRule(t, s, okBody); rec.Code != http.StatusOK {
		t.Fatalf("单事件源的 all 组合应允许: %d %s", rec.Code, rec.Body.String())
	}
}

// M3：冷却时间上限（24h）与负数校验。
func TestSaveRuleCooldownBounds(t *testing.T) {
	s := newRuleTestServer(t)
	base := `{"id":"r-cd","version":0,"name":"冷却","enabled":true,"matchMode":"anyone","monitors":[{"kind":"level","levels":[3]}],"controls":[{"kind":"device","deviceId":"pcs-001","serviceKey":"setPower","value":10}],"cooldownMs":%d}`
	if rec := postRule(t, s, strings.Replace(base, "%d", "-1", 1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("负冷却应 400: %d", rec.Code)
	}
	if rec := postRule(t, s, strings.Replace(base, "%d", "86400001", 1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("超上限冷却应 400: %d", rec.Code)
	}
	if rec := postRule(t, s, strings.Replace(base, "%d", "86400000", 1)); rec.Code != http.StatusOK {
		t.Fatalf("上限值应允许: %d %s", rec.Code, rec.Body.String())
	}
}

// M5：ID 字符集校验（避免含 "/" 的 ID 无法删除、以及危险的 id 注入面）。
func TestSaveRuleIDCharset(t *testing.T) {
	s := newRuleTestServer(t)
	for _, id := range []string{"a/b", "a b", "x');alert(1);//", strings.Repeat("a", 129)} {
		body := `{"id":"` + id + `","version":0,"name":"x","monitors":[{"kind":"level","levels":[3]}],"controls":[{"kind":"device","deviceId":"d","serviceKey":"s","value":1}]}`
		if rec := postRule(t, s, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("非法 ID %q 应 400，实际 %d", id, rec.Code)
		}
	}
}

// H7：kind 字段裁剪——level 源清空事件字段、event 源清空级别字段。
func TestSaveRuleTrimsKindFields(t *testing.T) {
	s := newRuleTestServer(t)
	body := `{"id":"r-trim","version":0,"name":"裁剪","enabled":true,"matchMode":"anyone",` +
		`"monitors":[{"kind":"level","levels":[3],"deviceId":"bms-001","eventKey":"temp_high","type":"device"},` +
		`{"kind":"event","deviceId":"bms-001","eventKey":"temp_high","levels":[1,2]}],` +
		`"controls":[{"kind":"device","deviceId":"pcs-001","serviceKey":"setPower","value":10}]}`
	if rec := postRule(t, s, body); rec.Code != http.StatusOK {
		t.Fatalf("保存应成功: %d %s", rec.Code, rec.Body.String())
	}
	var got store.AlarmRule
	s.DB.First(&got, "id = ?", "r-trim")
	if got.Monitors[0].DeviceID != "" || got.Monitors[0].EventKey != "" || got.Monitors[0].Type != "" {
		t.Fatalf("level 源应裁剪事件字段: %+v", got.Monitors[0])
	}
	if len(got.Monitors[1].Levels) != 0 {
		t.Fatalf("event 源应裁剪级别字段: %+v", got.Monitors[1])
	}
}

// stubRuntime 提供定义缓存快照（保护记录补全设备名/服务名）。
type stubRuntime struct {
	snapshot runtime.DefinitionsSnapshot
}

func (s stubRuntime) ReloadBus(config.App) error                       { return nil }
func (s stubRuntime) Restart(config.App) bool                          { return false }
func (s stubRuntime) DefinitionsSnapshot() runtime.DefinitionsSnapshot { return s.snapshot }
func (s stubRuntime) RefreshDefinitions() error                        { return nil }
func (s stubRuntime) Status() runtime.Status                           { return runtime.Status{} }
func (s stubRuntime) ResetRuleState(string)                            {}
func (s stubRuntime) InvalidateRules()                                 {}

// 保护记录展示口径：补全设备名/服务名/告警名（前端不再展示内部 ID）。
func TestListActionsEnriched(t *testing.T) {
	s := newRuleTestServer(t)
	s.Runtime = stubRuntime{snapshot: runtime.DefinitionsSnapshot{Devices: []runtime.DeviceDefinition{{
		Device:  natsclient.DeviceSummary{DeviceID: "pcs-001", DeviceName: "1号PCS"},
		Methods: []natsclient.MethodDefinition{{Key: "setPower", Name: "设置功率"}},
	}}}}
	now := time.Now().UTC()
	if err := s.DB.Create(&store.ActionRecord{
		ID: "ar-1", RuleID: "r1", RuleName: "降功率", AlarmID: "a-1",
		DeviceID: "pcs-001", ServiceKey: "setPower", Value: 0,
		Status: store.ActionAccepted, DispatchedAt: now,
	}).Error; err != nil {
		t.Fatalf("预置动作记录失败: %v", err)
	}
	if err := s.DB.Create(&store.Alarm{ID: "a-1", Name: "电池过温", State: store.AlarmStateActive, OccurredAt: now}).Error; err != nil {
		t.Fatalf("预置告警失败: %v", err)
	}

	rec := httptest.NewRecorder()
	s.ListActions(rec, httptest.NewRequest(http.MethodGet, "/api/actions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("查询应成功: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"deviceName":"1号PCS"`, `"serviceName":"设置功率"`, `"alarmName":"电池过温"`, `"value":0`} {
		if !strings.Contains(body, want) {
			t.Fatalf("保护记录未补全可读信息（缺 %s）: %s", want, body)
		}
	}
}
