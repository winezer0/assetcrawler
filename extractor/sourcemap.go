package extractor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// maxSourceMapEntries 限制单个 source map 的产出条数。
const maxSourceMapEntries = 500

// LooksLikeSourceMap 判断响应是否为 source map。
//
// 只看后缀与两个必需字段，不做完整校验：`version` 与 `sources` 同时出现
// 已经足够把 sourcemap 和普通 JSON 区分开，而误判的代价只是一次空提取。
func LooksLikeSourceMap(path string, body []byte) bool {
	if strings.HasSuffix(strings.ToLower(path), ".map") {
		return true
	}
	if len(body) < 16 {
		return false
	}
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte(`"sources"`)) && bytes.Contains(head, []byte(`"version"`))
}

// sourceMapDoc 只取需要的字段，body 里的 mappings 可能有几 MB，不解析。
type sourceMapDoc struct {
	Version    int      `json:"version"`
	SourceRoot string   `json:"sourceRoot"`
	Sources    []string `json:"sources"`
}

// FromSourceMap 从 source map 的 sources 里提取可访问的源码路径。
//
// 价值边界很明确：`sources` 里绝大多数是构建机上的源码（`../src/x.ts`、
// `node_modules/...`），部署后根本不存在。真正有用的是 webpack 伪协议里
// 带绝对路径的那一类——`webpack:///src/api/order.js` 经归一化后就是
// 站点根下的 `/src/api/order.js`，能直接访问。
//
// 所以只收「归一化后以 / 开头」的结果，其余一律跳过：宁可少收，不制造
// 一批必然 404 的源码路径。
func FromSourceMap(body []byte) []Request {
	var doc sourceMapDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	if len(doc.Sources) == 0 {
		return nil
	}
	out := make([]Request, 0, len(doc.Sources))
	for _, s := range doc.Sources {
		if len(out) >= maxSourceMapEntries {
			break
		}
		p, ok := normalizeSourcePath(s, doc.SourceRoot)
		if !ok {
			continue
		}
		out = append(out, Request{
			Raw: p, Method: http.MethodGet,
			Source: "sourcemap", Attribute: "sources", Tag: "sourcemap",
		})
	}
	return out
}

// pseudoProtocols 是 source map 里常见的伪协议。
//
// 它们后面的"主机"不是真的主机：`webpack:///src/a.js` 的三个斜杠表示空 host，
// `webpack://app/./src/a.js` 的 app 是构建产物名。
var pseudoProtocols = []string{"webpack://", "file://", "http://", "https://", "webpack-internal://"}

// normalizeSourcePath 把一条 sources 条目归一化成站点根下的路径。
//
//	webpack:///src/api/order.js     -> /src/api/order.js
//	webpack://app/./src/x.js        -> /src/x.js
//	../src/x.ts                     -> 跳过（相对构建目录，部署后不存在）
//	webpack:///./src/y.js           -> /src/y.js
func normalizeSourcePath(s, root string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}

	rest := s
	stripped := false
	for _, proto := range pseudoProtocols {
		if strings.HasPrefix(strings.ToLower(rest), proto) {
			rest = rest[len(proto):]
			stripped = true
			break
		}
	}

	// 没有伪协议时只接受绝对路径：`../src/a.ts`、`node_modules/x/index.js`
	// 都是构建机上的位置，拼到站点根下必然 404。
	if !stripped && !strings.HasPrefix(rest, "/") {
		return "", false
	}

	// 有 host 段时（`webpack://app/./src/x.js`），host 之后才是路径。
	if !strings.HasPrefix(rest, "/") {
		if i := strings.Index(rest, "/"); i >= 0 {
			rest = rest[i:]
		} else {
			return "", false
		}
	}

	rest = strings.TrimPrefix(rest, "/")
	rest = strings.TrimPrefix(rest, "./")
	if rest == "" {
		return "", false
	}

	// sourceRoot 是构建配置里的前缀，拼在路径前；只接受同样以 / 或 ./ 起头的形式。
	if root != "" {
		r := strings.Trim(strings.TrimSpace(root), "/")
		if r != "" && !strings.HasPrefix(rest, r+"/") {
			rest = r + "/" + rest
		}
	}

	return "/" + rest, true
}
