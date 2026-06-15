//go:build grpc

package transcode

import (
	"reflect"
	"testing"
)

func TestParseTemplateErrors(t *testing.T) {
	bad := []string{
		"",                 // empty
		"v1/items",         // no leading slash
		"/a/**/b",          // ** not last
		"/{=*}",            // empty field
		"/{name=**}/extra", // rest inside a variable, not last
	}
	for _, tmpl := range bad {
		if _, err := parseTemplate(tmpl); err == nil {
			t.Errorf("parseTemplate(%q) = nil error, want error", tmpl)
		}
	}
}

func TestTemplateMatch(t *testing.T) {
	cases := []struct {
		tmpl  string
		path  string
		want  map[string]string
		match bool
	}{
		{"/v1/items", "/v1/items", map[string]string{}, true},
		{"/v1/items", "/v1/items/", nil, false},
		{"/v1/items", "/v1/other", nil, false},
		{"/v1/items/{id}", "/v1/items/42", map[string]string{"id": "42"}, true},
		{"/v1/items/{id}", "/v1/items", nil, false},
		{"/v1/items/{id}", "/v1/items/42/sub", nil, false},
		{"/v1/{resource}/{id}", "/v1/books/9", map[string]string{"resource": "books", "id": "9"}, true},
		{"/v1/users/{user.id}", "/v1/users/7", map[string]string{"user.id": "7"}, true},
		{"/v1/*/info", "/v1/anything/info", map[string]string{}, true},
		{"/v1/{name=shelves/*}", "/v1/shelves/3", map[string]string{"name": "shelves/3"}, true},
		{"/v1/{name=shelves/*}", "/v1/aisles/3", nil, false},
		{"/files/{path=**}", "/files/a/b/c.txt", map[string]string{"path": "a/b/c.txt"}, true},
		{"/files/{path=**}", "/files", map[string]string{"path": ""}, true},
		{"/v1/items/{id}:verb", "/v1/items/42:verb", map[string]string{"id": "42"}, true},
		{"/v1/items/{id}:verb", "/v1/items/42", nil, false},
	}
	for _, c := range cases {
		tmpl, err := parseTemplate(c.tmpl)
		if err != nil {
			t.Errorf("parseTemplate(%q) error: %v", c.tmpl, err)
			continue
		}
		got, ok := tmpl.match(c.path)
		if ok != c.match {
			t.Errorf("%q.match(%q) ok = %v, want %v", c.tmpl, c.path, ok, c.match)
			continue
		}
		if ok && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q.match(%q) = %v, want %v", c.tmpl, c.path, got, c.want)
		}
	}
}
