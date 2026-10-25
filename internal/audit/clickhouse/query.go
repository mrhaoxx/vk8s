package clickhouse

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/names"
)

// tsParamLayout DateTime64(3) 参数与输出的格式（UTC）。
const tsParamLayout = "2006-01-02 15:04:05.000"

// selectColumns 列表查询取的列；request_object 用空串占位（只有详情才取体）。
const selectColumns = "ts, received_at, source, vcluster, id, stage, level, verb, user, groups, impersonated_user, source_ips, user_agent, " +
	"namespace, api_group, api_version, resource, subresource, name, request_uri, response_code, response_reason, response_message, " +
	"'' AS request_object, request_truncated, annotations, platform_action, platform_target"

const detailColumns = "ts, received_at, source, vcluster, id, stage, level, verb, user, groups, impersonated_user, source_ips, user_agent, " +
	"namespace, api_group, api_version, resource, subresource, name, request_uri, response_code, response_reason, response_message, " +
	"request_object, request_truncated, annotations, platform_action, platform_target"

// Schema 幂等迁移的三条语句：建库、建表、按配置设 TTL。
func Schema(db string, retentionDays int) []string {
	table := db + ".events"
	return []string{
		"CREATE DATABASE IF NOT EXISTS " + db,
		"CREATE TABLE IF NOT EXISTS " + table + ` (
  ts                DateTime64(3, 'UTC'),
  received_at       DateTime64(3, 'UTC'),
  source            LowCardinality(String),
  vcluster          LowCardinality(String),
  id                String,
  stage             LowCardinality(String),
  level             LowCardinality(String),
  verb              LowCardinality(String),
  user              String,
  groups            Array(String),
  impersonated_user String,
  source_ips        Array(String),
  user_agent        String,
  namespace         String,
  api_group         LowCardinality(String),
  api_version       LowCardinality(String),
  resource          LowCardinality(String),
  subresource       LowCardinality(String),
  name              String,
  request_uri       String,
  response_code     UInt16,
  response_reason   String,
  response_message  String,
  request_object    String,
  request_truncated UInt8,
  annotations       Map(String, String),
  platform_action   LowCardinality(String),
  platform_target   String,
  INDEX idx_user user TYPE bloom_filter GRANULARITY 4,
  INDEX idx_id id TYPE bloom_filter GRANULARITY 1
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (vcluster, ts, id)
TTL toDateTime(ts) + INTERVAL ` + strconv.Itoa(retentionDays) + " DAY",
		"ALTER TABLE " + table + " MODIFY TTL toDateTime(ts) + INTERVAL " + strconv.Itoa(retentionDays) + " DAY",
	}
}

// BuildQuery 列表查询：只为非空过滤项生成条件，值全部走类型化参数（params 的键不带 param_ 前缀）。
func BuildQuery(db string, f audit.Filter) (string, map[string]string) {
	params := map[string]string{
		"from": f.From.UTC().Format(tsParamLayout),
		"to":   f.To.UTC().Format(tsParamLayout),
	}
	var where []string
	where = append(where, "ts >= {from:DateTime64(3)}", "ts < {to:DateTime64(3)}")
	eq := func(col, name, val string) {
		if val == "" {
			return
		}
		where = append(where, col+" = {"+name+":String}")
		params[name] = val
	}
	eq("vcluster", "vcluster", f.VCluster)
	eq("user", "user", f.User)
	eq("verb", "verb", f.Verb)
	eq("resource", "resource", f.Resource)
	eq("namespace", "namespace", f.Namespace)
	eq("name", "name", f.Name)
	eq("source", "source", string(f.Source))
	if f.Token != "" {
		where = append(where, "annotations['"+names.TokenIDKey+"'] = {token:String}")
		params["token"] = f.Token
	}
	switch f.Outcome {
	case "success":
		where = append(where, "response_code < 400")
	case "failure":
		where = append(where, "response_code >= 400")
	}
	if !f.BeforeTS.IsZero() && f.BeforeID != "" {
		where = append(where, "(ts, id) < ({before_ts:DateTime64(3)}, {before_id:String})")
		params["before_ts"] = f.BeforeTS.UTC().Format(tsParamLayout)
		params["before_id"] = f.BeforeID
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	params["limit"] = strconv.Itoa(limit)
	sql := fmt.Sprintf("SELECT %s FROM %s.events WHERE %s ORDER BY ts DESC, id DESC LIMIT {limit:UInt32}",
		selectColumns, db, strings.Join(where, " AND "))
	return sql, params
}

// BuildGet 单条详情（含 request_object）；vcluster 非空时限定集群。
func BuildGet(db, id, vcluster string) (string, map[string]string) {
	params := map[string]string{"id": id}
	where := "id = {id:String}"
	if vcluster != "" {
		where += " AND vcluster = {vcluster:String}"
		params["vcluster"] = vcluster
	}
	return fmt.Sprintf("SELECT %s FROM %s.events WHERE %s ORDER BY ts DESC LIMIT 1", detailColumns, db, where), params
}

// parseTS 解析 ClickHouse 输出的 DateTime64(3)（UTC），兼容 RFC3339。
func parseTS(s string) (time.Time, error) {
	if t, err := time.ParseInLocation(tsParamLayout, s, time.UTC); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}
