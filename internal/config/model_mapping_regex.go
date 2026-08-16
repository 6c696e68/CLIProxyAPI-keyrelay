package config

import "regexp"

func compileModelMappingRegex(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(pattern)
}
