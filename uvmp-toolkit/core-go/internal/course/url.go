package course

import (
	"net/url"
	"strings"
)

// IsAPI URL 路径是否以 apiPath 结尾（trial.py _is_api 的 urlsplit.path.endswith 口径）。
func IsAPI(raw, apiPath string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(u.Path, apiPath)
}

// IsTrainDetailURL 是否培训班课程设置列表页（trial.py is_train_detail_url）：
// 主路径或 hash 路由（去 query、去尾斜杠）后缀匹配 trainDetail。
func IsTrainDetailURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.TrimRight(u.Path, "/")
	frag := u.Fragment
	if i := strings.Index(frag, "?"); i >= 0 {
		frag = frag[:i]
	}
	frag = strings.TrimRight(frag, "/")
	return strings.HasSuffix(path, TrainDetailPath) || strings.HasSuffix(frag, "/train/trainDetail")
}

// SafePageAddress 日志用安全地址（去 userinfo 与 query，trial.py safe_page_address）。
func SafePageAddress(raw string) string {
	if strings.HasPrefix(raw, "about:") {
		if i := strings.Index(raw, "?"); i >= 0 {
			return raw[:i]
		}
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := u.Host
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	address := u.Path
	if host != "" {
		address = u.Scheme + "://" + host + u.Path
	}
	if u.Fragment != "" {
		frag := u.Fragment
		if i := strings.Index(frag, "?"); i >= 0 {
			frag = frag[:i]
		}
		address += "#" + frag
	}
	if address == "" {
		return "(空地址)"
	}
	return address
}

// CourseListKey 培训班检查点键（trial.py course_list_key）：trainid 优先，否则页面地址。
func CourseListKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "page:" + SafePageAddress(raw)
	}
	query := u.Query()
	if i := strings.Index(u.Fragment, "?"); i >= 0 {
		if fq, err := url.ParseQuery(u.Fragment[i+1:]); err == nil {
			for k, vs := range fq {
				for _, v := range vs {
					query.Add(k, v)
				}
			}
		}
	}
	trainID := strings.TrimSpace(query.Get("trainid"))
	if trainID != "" {
		return "train:" + trainID
	}
	return "page:" + SafePageAddress(raw)
}

// joinURL 相对地址按基准解析（urljoin 同口径）。
func joinURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}
