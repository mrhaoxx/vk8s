// Package audit 定义审计事件行、写入管线与平台操作记录器（设计文档 2026-10-24 审计）。
package audit

import (
	"encoding/json"
	"time"
	"unicode/utf8"
)

// Source 事件来源。
type Source string

const (
	SourceKube     Source = "kube"     // 虚拟 apiserver 的审计 webhook
	SourcePlatform Source = "platform" // manager API 的变更操作与登录
)

// MaxRequestObjectBytes request_object 的字节上限，超出截断。
const MaxRequestObjectBytes = 64 << 10

// Event 一行审计记录，字段与 ClickHouse 表列一一对应（JSON 标签即列名）。
type Event struct {
	TS         time.Time `json:"ts"`
	ReceivedAt time.Time `json:"received_at"`
	Source     Source    `json:"source"`
	VCluster   string    `json:"vcluster"`
	ID         string    `json:"id"`

	Stage            string   `json:"stage"`
	Level            string   `json:"level"`
	Verb             string   `json:"verb"`
	User             string   `json:"user"`
	Groups           []string `json:"groups"`
	ImpersonatedUser string   `json:"impersonated_user"`
	SourceIPs        []string `json:"source_ips"`
	UserAgent        string   `json:"user_agent"`

	Namespace   string `json:"namespace"`
	APIGroup    string `json:"api_group"`
	APIVersion  string `json:"api_version"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource"`
	Name        string `json:"name"`
	RequestURI  string `json:"request_uri"`

	ResponseCode    uint16 `json:"response_code"`
	ResponseReason  string `json:"response_reason"`
	ResponseMessage string `json:"response_message"`

	RequestObject    string            `json:"request_object"`
	RequestTruncated uint8             `json:"request_truncated"`
	Annotations      map[string]string `json:"annotations"`

	PlatformAction string `json:"platform_action"`
	PlatformTarget string `json:"platform_target"`
}

// Truncate 按字节上限截断，不在多字节字符中间切开；返回 (结果, 是否截断)。
func Truncate(s string) (string, uint8) {
	if len(s) <= MaxRequestObjectBytes {
		return s, 0
	}
	cut := MaxRequestObjectBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], 1
}

// 时间输出为 RFC3339 毫秒（UTC），ClickHouse 以 best_effort 解析；nil 切片 / map 输出为空容器。
const tsLayout = "2006-01-02T15:04:05.000Z07:00"

type eventJSON struct {
	TS         string `json:"ts"`
	ReceivedAt string `json:"received_at"`
	Source     Source `json:"source"`
	VCluster   string `json:"vcluster"`
	ID         string `json:"id"`

	Stage            string   `json:"stage"`
	Level            string   `json:"level"`
	Verb             string   `json:"verb"`
	User             string   `json:"user"`
	Groups           []string `json:"groups"`
	ImpersonatedUser string   `json:"impersonated_user"`
	SourceIPs        []string `json:"source_ips"`
	UserAgent        string   `json:"user_agent"`

	Namespace   string `json:"namespace"`
	APIGroup    string `json:"api_group"`
	APIVersion  string `json:"api_version"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource"`
	Name        string `json:"name"`
	RequestURI  string `json:"request_uri"`

	ResponseCode    uint16 `json:"response_code"`
	ResponseReason  string `json:"response_reason"`
	ResponseMessage string `json:"response_message"`

	RequestObject    string            `json:"request_object"`
	RequestTruncated uint8             `json:"request_truncated"`
	Annotations      map[string]string `json:"annotations"`

	PlatformAction string `json:"platform_action"`
	PlatformTarget string `json:"platform_target"`
}

// MarshalJSON 见 eventJSON 的说明。
func (e Event) MarshalJSON() ([]byte, error) {
	groups, ips, ann := e.Groups, e.SourceIPs, e.Annotations
	if groups == nil {
		groups = []string{}
	}
	if ips == nil {
		ips = []string{}
	}
	if ann == nil {
		ann = map[string]string{}
	}
	return json.Marshal(eventJSON{
		TS: e.TS.UTC().Format(tsLayout), ReceivedAt: e.ReceivedAt.UTC().Format(tsLayout), Source: e.Source, VCluster: e.VCluster, ID: e.ID,
		Stage: e.Stage, Level: e.Level, Verb: e.Verb, User: e.User, Groups: groups, ImpersonatedUser: e.ImpersonatedUser, SourceIPs: ips, UserAgent: e.UserAgent,
		Namespace: e.Namespace, APIGroup: e.APIGroup, APIVersion: e.APIVersion, Resource: e.Resource, Subresource: e.Subresource, Name: e.Name, RequestURI: e.RequestURI,
		ResponseCode: e.ResponseCode, ResponseReason: e.ResponseReason, ResponseMessage: e.ResponseMessage,
		RequestObject: e.RequestObject, RequestTruncated: e.RequestTruncated, Annotations: ann,
		PlatformAction: e.PlatformAction, PlatformTarget: e.PlatformTarget,
	})
}
