package providerkit

import (
	"net/url"
	"regexp"
	"strings"
)

// endpointPlaceholder 是全仓唯一的 endpoint 占位符定义。首字符必须是字母，
// 所以 {9}、{_x} 都不是占位符：它们会被 ValidEndpointTemplate 判为非法花括号，
// 在 Registry 注册期就拒绝，运行期不可能出现未展开的花括号。
var endpointPlaceholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// EndpointPlaceholders 按出现顺序返回模板里的占位符名，可能重复。
func EndpointPlaceholders(template string) []string {
	matches := endpointPlaceholder.FindAllStringSubmatch(template, -1)
	keys := make([]string, 0, len(matches))
	for _, match := range matches {
		keys = append(keys, match[1])
	}
	return keys
}

// ValidEndpointTemplate 报告模板里的花括号是否全部构成合法占位符。
func ValidEndpointTemplate(template string) bool {
	return !strings.ContainsAny(
		endpointPlaceholder.ReplaceAllString(template, ""),
		"{}",
	)
}

// ExpandEndpoint 展开 endpoint 模板：取到的值按所处位置做 path / query 转义，
// 占位符因此只能影响 path 与 query，不能改变 scheme、hostname 或 port。value
// 返回空串的占位符原样保留并按出现顺序计入 missing，由调用方决定这是错误
// （授权流程）还是「配置尚未就绪」（policy 投影）。
func ExpandEndpoint(
	template string,
	value func(key string) string,
) (string, []string) {
	var missing []string
	var builder strings.Builder
	builder.Grow(len(template))
	cursor := 0
	query := strings.IndexByte(template, '?')
	for _, match := range endpointPlaceholder.FindAllStringSubmatchIndex(
		template,
		-1,
	) {
		builder.WriteString(template[cursor:match[0]])
		key := template[match[2]:match[3]]
		switch v := value(key); {
		case v == "":
			missing = append(missing, key)
			builder.WriteString(template[match[0]:match[1]])
		case query >= 0 && match[0] > query:
			builder.WriteString(url.QueryEscape(v))
		default:
			builder.WriteString(url.PathEscape(v))
		}
		cursor = match[1]
	}
	builder.WriteString(template[cursor:])
	return builder.String(), missing
}
