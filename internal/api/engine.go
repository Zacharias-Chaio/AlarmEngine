package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"alarmengine/internal/store"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// ListRules 返回全部告警引擎规则。
func (s *Server) ListRules(w http.ResponseWriter, r *http.Request) {
	var list []store.AlarmRule
	if err := s.DB.Order("id asc").Find(&list).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, list)
}

// SaveRule 创建或更新规则（upsert）。
func (s *Server) SaveRule(w http.ResponseWriter, r *http.Request) {
	var rule store.AlarmRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		fail(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if strings.TrimSpace(rule.ID) == "" {
		fail(w, http.StatusBadRequest, "缺少 ID")
		return
	}
	if strings.TrimSpace(rule.Name) == "" {
		fail(w, http.StatusBadRequest, "缺少规则名称")
		return
	}
	var exist store.AlarmRule
	if err := s.DB.First(&exist, "id = ?", rule.ID).Error; err == nil {
		rule.CreatedAt = exist.CreatedAt
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.Save(&rule).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, rule)
}

// DeleteRule 删除指定规则。
func (s *Server) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res := s.DB.Delete(&store.AlarmRule{}, "id = ?", id)
	if res.Error != nil {
		fail(w, http.StatusInternalServerError, res.Error.Error())
		return
	}
	if res.RowsAffected == 0 {
		fail(w, http.StatusNotFound, "规则不存在: id="+id)
		return
	}
	ok(w, map[string]string{"id": id})
}
