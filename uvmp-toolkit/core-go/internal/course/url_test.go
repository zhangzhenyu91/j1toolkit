package course

import "testing"

func TestIsTrainDetailURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://sgcc.example/learning-c/train/trainDetail?trainid=1", true},
		{"https://sgcc.example/learning-c/train/trainDetail/", true},
		{"https://sgcc.example/x#/learning-c/train/trainDetail?id=1", true},
		{"https://sgcc.example/#/train/trainDetail?trainid=9", true},
		{"https://sgcc.example/learning-c/train/list", false},
		{"about:blank", false},
	}
	for _, c := range cases {
		if got := IsTrainDetailURL(c.url); got != c.want {
			t.Fatalf("IsTrainDetailURL(%q) = %v，want %v", c.url, got, c.want)
		}
	}
}

func TestSafePageAddress(t *testing.T) {
	if got := SafePageAddress("about:blank?x=1"); got != "about:blank" {
		t.Fatalf("about 地址 = %q", got)
	}
	got := SafePageAddress("https://user:pass@host/path?q=1#frag?x=2")
	if got != "https://host/path#frag" {
		t.Fatalf("安全地址 = %q", got)
	}
	if SafePageAddress("") != "(空地址)" {
		t.Fatal("空地址文案错误")
	}
}

func TestCourseListKey(t *testing.T) {
	if got := CourseListKey("https://h/p?trainid=train1"); got != "train:train1" {
		t.Fatalf("trainid 键 = %q", got)
	}
	if got := CourseListKey("https://h/p#/x?trainid=train2"); got != "train:train2" {
		t.Fatalf("hash query 键 = %q", got)
	}
	if got := CourseListKey("https://h/p?page=1"); got != "page:https://h/p" {
		t.Fatalf("地址键 = %q", got)
	}
}

func TestIsAPI(t *testing.T) {
	if !IsAPI("https://h/rest/x/learningc/trainclass/course/page?a=1", CoursePageAPI) {
		t.Fatal("API 后缀应匹配")
	}
	if IsAPI("https://h/other", CoursePageAPI) {
		t.Fatal("不应匹配")
	}
}

func TestJoinURL(t *testing.T) {
	if got := joinURL("https://h/a/b", "/c"); got != "https://h/c" {
		t.Fatalf("join = %q", got)
	}
	if got := joinURL("https://h/a/b", "d"); got != "https://h/a/d" {
		t.Fatalf("join 相对 = %q", got)
	}
}
