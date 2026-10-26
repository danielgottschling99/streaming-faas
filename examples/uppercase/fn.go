package main

import "strings"

func fn(body string, headers map[string]string) (string, error) {
	return strings.ToUpper(body), nil
}
