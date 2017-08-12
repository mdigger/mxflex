package main

import (
	"sort"
	"strings"
)

type extList struct {
	list map[string]struct{}
}

func (l extList) Len() int {
	return len(l.list)
}

func (l extList) List() []string {
	var list = make([]string, 0, len(l.list))
	for ext := range l.list {
		list = append(list, ext)
	}
	sort.Strings(list)
	return list
}

func (l extList) Get() interface{} {
	return l.List()
}

func (l extList) String() string {
	return strings.Join(l.Get().([]string), ",")
}

func (l *extList) Set(exts string) error {
	items := strings.Split(exts, ",")
	if l.list == nil {
		l.list = make(map[string]struct{}, len(items))
	}
	for _, ext := range items {
		l.list[strings.TrimSpace(ext)] = struct{}{}
	}
	return nil
}
