package course

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// mockStation 模拟国网学堂站点（output/course_mock_e2e.py Handler 的 Go 移植）：
// 课程列表分页 / 课件清单 / 学习上报 / 课件切换 / 文档预览，train1 含视频+文档+卡住课，
// train2 含一门文档课；getPassWare 对卡住课不上报进度。JSON 给 fake 驱动，HTML 给
// 真实浏览器集成测试（只访问本机，不连国网学堂）。
type mockStation struct {
	mu       sync.Mutex
	trains   map[string][]*mockCourse
	pageSize int
	server   *httptest.Server
}

// mockCourse 一门模拟课程。
type mockCourse struct {
	ResourceID string
	Name       string
	StudyRate  float64
	Kind       string // video / doc / stuck
}

func newMockStation() *mockStation {
	s := &mockStation{
		trains: map[string][]*mockCourse{
			"train1": {
				{ResourceID: "v", Name: "模拟视频课", Kind: "video"},
				{ResourceID: "d", Name: "模拟文档课", Kind: "doc"},
				{ResourceID: "s", Name: "模拟系统卡住课", StudyRate: 50, Kind: "stuck"},
			},
			"train2": {
				{ResourceID: "x", Name: "另一培训班文档课", Kind: "doc"},
			},
		},
		pageSize: 2,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	s.server = httptest.NewServer(mux)
	return s
}

func (s *mockStation) Close() { s.server.Close() }

// BaseURL 站点根地址。
func (s *mockStation) BaseURL() string { return s.server.URL }

// ListURL 培训班课程设置列表页地址（find_list_page 识别用）。
func (s *mockStation) ListURL(train string) string {
	return s.server.URL + "/learning-c/train/trainDetail?trainid=" + train
}

func (s *mockStation) route(w http.ResponseWriter, r *http.Request) {
	u, _ := url.Parse(r.URL.Path)
	q := r.URL.Query()
	switch {
	case strings.HasSuffix(u.Path, TrainDetailPath):
		s.write(w, 200, listHTML(q.Get("trainid")), "text/html; charset=utf-8")
	case strings.HasSuffix(u.Path, CoursePageAPI):
		s.writeJSON(w, s.pageData(q.Get("trainid"), atoi(q.Get("page"), 1)))
	case u.Path == "/learning-c/course/courseplay":
		s.write(w, 200, playHTML(q.Get("id"), s.course(q.Get("id"))), "text/html; charset=utf-8")
	case strings.HasSuffix(u.Path, WareListAPI):
		s.writeJSON(w, s.warePayload(q.Get("id")))
	case strings.HasSuffix(u.Path, LastStudyAPI):
		s.writeJSON(w, 1)
	case u.Path == "/fcscloud/view/preview/mock":
		s.write(w, 200, "<!doctype html><html><body>文档预览已加载</body></html>", "text/html; charset=utf-8")
	default:
		if r.Method == http.MethodPost && strings.HasSuffix(u.Path, SaveStudyAPI) {
			var body struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.passWare(body.ID)
			s.writeJSON(w, 1)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *mockStation) write(w http.ResponseWriter, status int, body, ctype string) {
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (s *mockStation) writeJSON(w http.ResponseWriter, value any) {
	data, _ := json.Marshal(map[string]any{"resultCode": "200", "resultValue": value})
	s.write(w, 200, string(data), "application/json; charset=utf-8")
}

func (s *mockStation) course(id string) *mockCourse {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range s.trains {
		for _, c := range list {
			if c.ResourceID == id {
				return c
			}
		}
	}
	return nil
}

// pageData 课程分页（公开字段同 Python public_course）。
func (s *mockStation) pageData(train string, page int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.trains[train]
	start := (page - 1) * s.pageSize
	end := start + s.pageSize
	if start > len(list) {
		start = len(list)
	}
	if end > len(list) {
		end = len(list)
	}
	items := []any{}
	for _, c := range list[start:end] {
		items = append(items, map[string]any{
			"resourceid":   c.ResourceID,
			"coursename":   c.Name,
			"studyRate":    c.StudyRate,
			"contenttype":  float64(1),
			"hour":         float64(1),
			"courseStatus": float64(1),
			"coursetype":   float64(1),
			"coursespace":  float64(1),
		})
	}
	return map[string]any{"list": items, "total": len(list)}
}

// warePayload 课件清单（目录树嵌套，叶子带 wareType）。
func (s *mockStation) warePayload(id string) []any {
	c := s.course(id)
	if c == nil {
		return []any{}
	}
	wareType := 2.0
	suffix := ".pdf"
	if c.Kind == "video" {
		wareType = 1
		suffix = ".mp4"
	}
	ware := map[string]any{
		"id":         id,
		"courseId":   id,
		"resId":      id,
		"wareName":   c.Name,
		"resName":    c.Name + suffix,
		"wareType":   wareType,
		"suffixInfo": suffix,
	}
	return []any{map[string]any{"id": "catalog", "courseWareCatalogRelationDTOS": []any{ware}}}
}

// passWare 学习上报：非卡住课置 100%（kind=stuck 不动）。
func (s *mockStation) passWare(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range s.trains {
		for _, c := range list {
			if c.ResourceID == id && c.Kind != "stuck" {
				c.StudyRate = 100
			}
		}
	}
}

// rateOf 当前进度（断言用）。
func (s *mockStation) rateOf(id string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range s.trains {
		for _, c := range list {
			if c.ResourceID == id {
				return c.StudyRate
			}
		}
	}
	return -1
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// listHTML 列表页（真实浏览器集成测试用；DOM 结构与模拟站 JS 同契约）。
func listHTML(train string) string {
	return fmt.Sprintf(`<!doctype html><html><body>
<button id="reset" onclick="loadPage(1)">重置</button>
<div class="el-tabs"><div class="el-tabs__item is-active">课程设置</div></div>
<div class="el-table"><table class="el-table__header"><tr><th>课程名称</th><th>学习进度</th><th>进入学习</th></tr></table>
<div class="el-table__body-wrapper"><table><tbody></tbody></table></div></div>
<div class="el-pagination"><button class="btn-next" onclick="nextPage()">下一页</button><ul class="el-pager"><li class="active">1</li></ul>
<span class="el-pagination__jump">前往<input onkeyup="jumpPage(event)">页</span></div>
<script>
let currentPage = 1;
let total = 0;
function loadPage(page) {
  fetch('/rest/gwxt-learning-c-s/learningc/trainclass/course/page?trainid=%s&page=' + page)
    .then(r => r.json()).then(data => {
      currentPage = page;
      total = data.resultValue.total;
      document.querySelector('.el-table__body-wrapper tbody').innerHTML = data.resultValue.list.map(c =>
        '<tr class="el-table__row"><td>' + c.coursename + '</td><td>' + c.studyRate + '%%</td>' +
        '<td><span onclick="openCourse(\'' + c.resourceid + '\')">进入学习</span></td></tr>'
      ).join('');
      document.querySelector('.el-pager .active').textContent = String(page);
      document.querySelector('.btn-next').disabled = page * 2 >= total;
    });
}
function nextPage() { loadPage(currentPage + 1); }
function jumpPage(event) { if (event.key === 'Enter') loadPage(Number(event.target.value)); }
function openCourse(id) { window.open('/learning-c/course/courseplay?id=' + id, '_blank'); }
loadPage(1);
</script></body></html>`, train)
}

// playHTML 学习弹窗页（视频/文档两种课件流程；视频用属性覆盖模拟短片）。
func playHTML(id string, c *mockCourse) string {
	kind := "doc"
	if c != nil {
		kind = c.Kind
	}
	body := `<iframe src="/fcscloud/view/preview/mock" style="width:600px;height:300px"></iframe>
<script>setTimeout(() => fetch('/rest/gwxt-learning-c-s/learningc/course/study-record-info/getPassWare', {
  method: 'POST', body: JSON.stringify({id: '__ID__'})
}), 200);</script>`
	if kind == "video" {
		body = `<video style="width:640px;height:120px"></video>
<div role="slider" aria-label="进度小节" style="width:640px;height:24px;background:#ccc">进度小节</div>
<script>
const video = document.querySelector('video');
const slider = document.querySelector('[role=slider]');
let current = 0;
Object.defineProperties(video, {
  duration: {get: () => 10}, readyState: {get: () => 1},
  currentTime: {get: () => current, set: value => { current = value; }},
  ended: {get: () => current >= 10}, paused: {get: () => false}
});
function seek(event) {
  const rect = slider.getBoundingClientRect();
  video.currentTime = Math.max(0, Math.min(10, (event.clientX - rect.x) / rect.width * 10));
}
slider.addEventListener('mousedown', seek);
slider.addEventListener('mousemove', event => { if (event.buttons) seek(event); });
video.play = () => {
  return new Promise(resolve => {
    setTimeout(() => {
      video.currentTime = 10;
      video.dispatchEvent(new Event('ended'));
      fetch('/rest/gwxt-learning-c-s/learningc/course/study-record-info/getPassWare', {
        method: 'POST', body: JSON.stringify({id: '__ID__'})
      }).then(() => resolve());
    }, 100);
  });
};
</script>`
	}
	return `<!doctype html><html><body><div class="el-tree"><span class="nameClass">课件</span></div>` +
		strings.ReplaceAll(body, "__ID__", id) +
		`<script>fetch('/rest/gwxt-learning-c-s/learningc/course/ware-catalog-relation/listRes?id=` + id + `')</script></body></html>`
}
