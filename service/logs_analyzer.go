package service

import "strings"

func NewKeyWordMatcher(keywords []string) LogAnalyzer {
	return func(logLine string) bool {
		normalizedLogLine := strings.ToLower(logLine)

		for _, keyword := range keywords {
			if strings.Contains(normalizedLogLine, keyword) {
				return true
			}
		}

		return false
	}
}
